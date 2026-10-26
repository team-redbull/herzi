/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// herzi keeps each InfraEnv's pool of spare machines matched to NodePool demand.
// See docs/design/herzi-design.md.
package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/controller"
	"github.com/team-redbull/herzi/internal/installer"
	"github.com/team-redbull/herzi/internal/metrics"
	"github.com/team-redbull/herzi/internal/temporalx"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	// +kubebuilder:scaffold:imports
)

// version is set at build time: -ldflags "-X main.version=...".
var version = "dev"

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	// InfraEnv, NodePool and Agent are read as unstructured and need no scheme registration.
	// +kubebuilder:scaffold:scheme
}

// leaderElectionID names the Lease. It is stable across releases on purpose.
const leaderElectionID = "herzi.redbull.io"

// Environment variables for material that must not live in the ConfigMap:
// CA_FILE is an optional PEM CA bundle; CERT_FILE and KEY_FILE are optional
// paired mTLS PEM files. API_KEY authenticates Temporal; API_BEARER_TOKEN
// authenticates install-server. Values are never logged. Kubernetes client-go
// also honours KUBECONFIG and the standard in-cluster service environment.
const (
	envTemporalCA     = "HERZI_TEMPORAL_TLS_CA_FILE"
	envTemporalCert   = "HERZI_TEMPORAL_TLS_CERT_FILE"
	envTemporalKey    = "HERZI_TEMPORAL_TLS_KEY_FILE"
	envTemporalAPIKey = "HERZI_TEMPORAL_API_KEY"
	envBearerToken    = "HERZI_API_BEARER_TOKEN"
)

// startupBoundChanged lists the hub settings that were captured when the process started (the cache
// namespace, the Temporal client, the sweeper, the installer). A reload cannot apply them.
func startupBoundChanged(old, current config.HubConfig) []string {
	var changed []string
	add := func(name string, different bool) {
		if different {
			changed = append(changed, name)
		}
	}
	add("hubName", old.HubName != current.HubName)
	add("agentNamespace", old.AgentNamespace != current.AgentNamespace)
	add("installURL", old.InstallURL != current.InstallURL)
	add("maxSlots", old.MaxSlots != current.MaxSlots)
	add("temporalAddress", old.TemporalAddress != current.TemporalAddress)
	add("temporalNamespace", old.TemporalNamespace != current.TemporalNamespace)
	add("activityQueue", old.ActivityQueue != current.ActivityQueue)
	add("workflowQueue", old.WorkflowQueue != current.WorkflowQueue)
	add("segmentQueue", old.SegmentQueue != current.SegmentQueue)
	return changed
}

// nolint:gocyclo
func main() { os.Exit(run(os.Args[1:])) }

// run owns resource cleanup. Only main calls os.Exit, after run's defers finish.
func run(args []string) int {
	o, err := parseLaunchOptions(args)
	if err != nil {
		return 2
	}

	if o.showVersion {
		fmt.Println("herzi", version)
		return 0
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&o.log)))

	// Without a valid configuration there is nothing safe to do: fail fast and loudly.
	raw, err := os.ReadFile(o.configPath)
	if err != nil {
		setupLog.Error(nil, "Could not read the configuration file", "path", o.configPath)
		return 1
	}
	initial, err := config.Load(raw)
	if err != nil {
		setupLog.Error(nil, "The configuration file is invalid", "path", o.configPath)
		return 1
	}
	store := config.NewStore(initial)
	hub := initial.HubConfig

	metricsServerOptions := metricsOptions(o)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(),
		managerOptions(hub, metricsServerOptions, o.probeAddr, o.enableLeaderElection))
	if err != nil {
		setupLog.Error(nil, "Failed to start manager")
		return 1
	}

	// Temporal: a lazy client, so the pod starts and serves probes even while Temporal is down. An
	// unreachable Temporal then shows up as a sweep or poller error, which blocks the affected pools.
	tc, err := temporalx.Dial(temporalx.DialOptions{
		Address:   hub.TemporalAddress,
		Namespace: hub.TemporalNamespace,
		CAFile:    os.Getenv(envTemporalCA),
		CertFile:  os.Getenv(envTemporalCert),
		KeyFile:   os.Getenv(envTemporalKey),
		APIKey:    os.Getenv(envTemporalAPIKey),
		Identity:  "herzi/" + version + "@" + hub.HubName,
	})
	if err != nil {
		setupLog.Error(nil, "Failed to create the Temporal client")
		return 1
	}
	defer tc.Close()

	m := metrics.New(ctrlmetrics.Registry)
	m.SetConfigValid(true)

	sweeper := temporalx.NewSweeper(temporalx.Options{
		Client: tc, MCE: hub.HubName, MaxSlots: hub.MaxSlots,
		CacheTTL: 10 * time.Second, // Matches the controller spending throttle.
	})
	queues := temporalx.NewQueues(temporalx.Options{
		Client:        tc,
		ActivityQueue: hub.ActivityQueue, WorkflowQueue: hub.WorkflowQueue, SegmentQueue: hub.SegmentQueue,
	})
	inst, err := installer.New(installer.Options{
		URL: hub.InstallURL, MCECluster: hub.HubName, Namespace: hub.AgentNamespace,
		Timeout: installTimeout(hub.ReconcileTimeout), BearerToken: os.Getenv(envBearerToken),
	})
	if err != nil {
		setupLog.Error(nil, "Failed to create the installer client")
		return 1
	}

	// A config change re-enqueues every pool. The channel holds at most one pending signal.
	reload := make(chan event.GenericEvent, 1)
	watcher := &config.FileWatcher{
		Path: o.configPath, Store: store,
		OnUpdate: reloadHandler(hub, store, m, reload, setupLog, o.configPath),
	}

	r, err := controller.New(controller.Options{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Config: store,
		Sweeper: sweeper, Queues: queues, Installer: inst, Metrics: m, Reload: reload,
	})
	if err != nil {
		setupLog.Error(nil, "Failed to create the controller")
		return 1
	}
	if err := r.SetupWithManager(mgr); err != nil {
		setupLog.Error(nil, "Failed to set up the controller")
		return 1
	}
	if err := mgr.Add(watcher); err != nil {
		setupLog.Error(nil, "Failed to add the configuration watcher")
		return 1
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(nil, "Failed to set up health check")
		return 1
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(nil, "Failed to set up ready check")
		return 1
	}

	setupLog.Info("Starting manager", "version", version, "hub", hub.HubName, "agentNamespace", hub.AgentNamespace)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(nil, "Failed to run manager")
		return 1
	}
	return 0
}

// managerOptions keeps identity/cache/leadership wiring reviewable without
// binding sockets. No graceful drain is allowed after a lost lease.
func managerOptions(
	hub config.HubConfig, metricsOptions metricsserver.Options, probeAddr string, leader bool,
) ctrl.Options {
	shutdown := time.Duration(0)
	return ctrl.Options{
		Scheme: scheme, Metrics: metricsOptions, HealthProbeBindAddress: probeAddr,
		LeaderElection: leader, LeaderElectionID: leaderElectionID,
		LeaderElectionReleaseOnCancel: true, GracefulShutdownTimeout: &shutdown,
		Client: client.Options{Cache: &client.CacheOptions{Unstructured: true}},
		Cache:  controller.CacheOptions(hub.AgentNamespace),
	}
}

// installTimeout must stay positive even for the smallest valid duration; zero
// would activate the installer's unrelated 30s default. The parent ctx is also a
// hard upper bound, including live changes to ReconcileTimeout.
func installTimeout(timeout time.Duration) time.Duration { return max(time.Nanosecond, timeout/2) }

// reloadHandler sends accepted changes only and reports a restart requirement
// once per changed field set, rather than repeating it on unrelated pool edits.
func reloadHandler(
	hub config.HubConfig, store *config.Store, m *metrics.Metrics,
	reload chan<- event.GenericEvent, logger logr.Logger, path string,
) func(error) {
	var mu sync.Mutex
	lastRestart := ""
	return func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			m.SetConfigValid(false)
			// Config decoder errors may include arbitrary YAML scalar values.
			logger.Error(errors.New("invalid configuration"),
				"Configuration update rejected, keeping the last good configuration", "path", path)
			return
		}
		m.SetConfigValid(true)
		changed := startupBoundChanged(hub, store.Snapshot().HubConfig)
		key := strings.Join(changed, ",")
		if key != "" && key != lastRestart {
			logger.Info("Hub settings changed that need a restart to apply", "settings", changed)
		}
		lastRestart = key
		select {
		case reload <- event.GenericEvent{Object: &unstructured.Unstructured{}}:
		default:
		}
		logger.Info("Configuration reloaded", "path", path)
	}
}

type launchOptions struct {
	configPath, metricsAddr, metricsCertPath, metricsCertName, metricsCertKey, probeAddr string
	enableLeaderElection, secureMetrics, enableHTTP2, showVersion                        bool
	log                                                                                  zap.Options
}

func parseLaunchOptions(args []string) (launchOptions, error) {
	var o launchOptions
	flags := flag.NewFlagSet("herzi", flag.ContinueOnError)
	flags.StringVar(&o.configPath, "config", "/etc/herzi/config.yaml",
		"Path of the herzi configuration file (a mounted ConfigMap).")
	flags.StringVar(&o.metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flags.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flags.BoolVar(&o.enableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flags.BoolVar(&o.secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flags.StringVar(&o.metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flags.StringVar(&o.metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flags.StringVar(&o.metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flags.BoolVar(&o.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics server")
	flags.BoolVar(&o.showVersion, "version", false, "Print the version and exit.")
	o.log = zap.Options{Development: false}
	o.log.BindFlags(flags)
	clientconfig.RegisterFlags(flags) // Retain the documented controller-runtime --kubeconfig flag.
	return o, flags.Parse(args)
}

func metricsOptions(o launchOptions) metricsserver.Options {
	var tlsOpts []func(*tls.Config)
	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		c.NextProtos = []string{"http/1.1"}
	}
	if !o.enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	metricsServerOptions := metricsserver.Options{
		BindAddress:   o.metricsAddr,
		SecureServing: o.secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if o.secureMetrics {
		// Only authorised users and service accounts may read the metrics (see config/rbac).
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	if len(o.metricsCertPath) > 0 {
		metricsServerOptions.CertDir = o.metricsCertPath
		metricsServerOptions.CertName = o.metricsCertName
		metricsServerOptions.KeyName = o.metricsCertKey
	}

	return metricsServerOptions
}
