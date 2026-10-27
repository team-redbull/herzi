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

// Package main runs the herzi pool controller.
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

var version = "dev"

var commit = "none"

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// Keep the Lease name stable across releases to prevent competing leaders during upgrades.
const leaderElectionID = "herzi.redbull.io"

const (
	envTemporalCA     = "HERZI_TEMPORAL_TLS_CA_FILE"
	envTemporalCert   = "HERZI_TEMPORAL_TLS_CERT_FILE"
	envTemporalKey    = "HERZI_TEMPORAL_TLS_KEY_FILE"
	envTemporalAPIKey = "HERZI_TEMPORAL_API_KEY"
	envBearerToken    = "HERZI_API_BEARER_TOKEN"
)

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
	add("stuckAfter", old.StuckAfter != current.StuckAfter)
	add("temporalAddress", old.TemporalAddress != current.TemporalAddress)
	add("temporalNamespace", old.TemporalNamespace != current.TemporalNamespace)
	add("activityQueue", old.ActivityQueue != current.ActivityQueue)
	add("workflowQueue", old.WorkflowQueue != current.WorkflowQueue)
	add("segmentQueue", old.SegmentQueue != current.SegmentQueue)
	return changed
}

// nolint:gocyclo // Entry point delegates lifecycle management to run.
func main() { os.Exit(run(os.Args[1:])) }

// Only main calls os.Exit, so run's deferred cleanup completes first.
func run(args []string) int {
	o, err := parseLaunchOptions(args)
	if err != nil {
		return 2
	}

	if o.showVersion {
		fmt.Println("herzi", version, commit)
		return 0
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&o.log)))

	raw, err := os.ReadFile(o.configPath)
	if err != nil {
		setupLog.Error(err, "Could not read the configuration file", "path", o.configPath)
		return 1
	}
	initial, err := config.Load(raw)
	if err != nil {
		setupLog.Error(err, "The configuration file is invalid", "path", o.configPath)
		return 1
	}
	store := config.NewStore(initial)
	hub := initial.HubConfig

	metricsServerOptions := metricsOptions(o)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(),
		managerOptions(hub, metricsServerOptions, o.probeAddr, o.enableLeaderElection))
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		return 1
	}

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
		setupLog.Error(err, "Failed to create the Temporal client")
		return 1
	}
	defer tc.Close()

	m := metrics.New(ctrlmetrics.Registry)
	m.SetConfigValid(true)

	sweeper := temporalx.NewSweeper(temporalx.Options{
		Client: tc, MCE: hub.HubName, MaxSlots: hub.MaxSlots,
		CacheTTL: 10 * time.Second,
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
		setupLog.Error(err, "Failed to create the installer client")
		return 1
	}

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
		setupLog.Error(err, "Failed to create the controller")
		return 1
	}
	if err := r.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to set up the controller")
		return 1
	}
	if err := mgr.Add(watcher); err != nil {
		setupLog.Error(err, "Failed to add the configuration watcher")
		return 1
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		return 1
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		return 1
	}

	setupLog.Info("Starting manager", "version", version, "commit", commit,
		"hub", hub.HubName, "agentNamespace", hub.AgentNamespace)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		return 1
	}
	return 0
}

func managerOptions(
	hub config.HubConfig, metricsOptions metricsserver.Options, probeAddr string, leader bool,
) ctrl.Options {
	shutdown := time.Duration(0)
	return ctrl.Options{
		Scheme: scheme, Metrics: metricsOptions, HealthProbeBindAddress: probeAddr,
		LeaderElection: leader, LeaderElectionID: leaderElectionID,
		// Releasing the lease is safe because main exits as soon as the manager stops.
		LeaderElectionReleaseOnCancel: true, GracefulShutdownTimeout: &shutdown,
		Client: client.Options{Cache: &client.CacheOptions{Unstructured: true}},
		Cache:  controller.CacheOptions(hub.AgentNamespace),
	}
}

// Keep the timeout positive; zero would activate the installer's unrelated 30s default.
func installTimeout(timeout time.Duration) time.Duration { return max(time.Nanosecond, timeout/2) }

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
			// Config decoder errors may expose credentials in arbitrary YAML scalar values.
			diagnostic := errors.New("invalid configuration")
			if migration, ok := errors.AsType[*config.RemovedKeyError](err); ok {
				diagnostic = migration
			}
			logger.Error(diagnostic,
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
	clientconfig.RegisterFlags(flags)
	return o, flags.Parse(args)
}

func metricsOptions(o launchOptions) metricsserver.Options {
	var tlsOpts []func(*tls.Config)
	// Disable HTTP/2 by default to avoid Stream Cancellation and Rapid Reset attacks.
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
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	if len(o.metricsCertPath) > 0 {
		metricsServerOptions.CertDir = o.metricsCertPath
		metricsServerOptions.CertName = o.metricsCertName
		metricsServerOptions.KeyName = o.metricsCertKey
	}

	return metricsServerOptions
}
