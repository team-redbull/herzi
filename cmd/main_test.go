package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/metrics"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestStartupBoundChanged(t *testing.T) {
	base := config.DefaultHub()
	base.HubName, base.InstallURL, base.TemporalAddress = "ocp4-a", "https://w/x", "t:7233"
	base.ActivityQueue = "server-lifecycle-activity-ocp4-a"

	t.Run("identical", func(t *testing.T) {
		if got := startupBoundChanged(base, base); len(got) != 0 {
			t.Fatalf("want none, got %v", got)
		}
	})
	t.Run("live settings are not startup-bound", func(t *testing.T) {
		c := base
		c.Interval, c.ReconcileTimeout, c.AmbiguousWait = time.Hour, time.Hour, time.Hour
		if got := startupBoundChanged(base, c); len(got) != 0 {
			t.Fatalf("timing settings apply live, got %v", got)
		}
	})
	t.Run("each bound setting is reported", func(t *testing.T) {
		c := base
		c.HubName, c.AgentNamespace, c.InstallURL, c.MaxSlots = "ocp4-b", "other", "https://w/y", 50
		c.StuckAfter = time.Hour
		c.TemporalAddress, c.TemporalNamespace = "t2:7233", "ns"
		c.ActivityQueue, c.WorkflowQueue, c.SegmentQueue = "a", "b", "c"
		want := []string{"hubName", "agentNamespace", "installURL", "maxSlots", "stuckAfter", "temporalAddress",
			"temporalNamespace", "activityQueue", "workflowQueue", "segmentQueue"}
		if got := startupBoundChanged(base, c); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

func TestInstallTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{time.Nanosecond, 2 * time.Nanosecond, 3 * time.Nanosecond, time.Second, time.Minute} {
		got := installTimeout(timeout)
		require.Positive(t, got)
		require.LessOrEqual(t, got, timeout)
		if timeout > time.Nanosecond {
			require.Equal(t, timeout/2, got)
		}
	}
}
func TestManagerSafetyOptions(t *testing.T) {
	opts := managerOptions(config.DefaultHub(), metricsserver.Options{BindAddress: "0"}, "0", true)
	require.True(t, opts.LeaderElection)
	require.Equal(t, "herzi.redbull.io", opts.LeaderElectionID)
	require.True(t, opts.LeaderElectionReleaseOnCancel)
	require.NotNil(t, opts.GracefulShutdownTimeout)
	require.Zero(t, *opts.GracefulShutdownTimeout)
	require.True(t, opts.Client.Cache.Unstructured)
	require.Len(t, opts.Cache.ByObject, 3)
}
func TestInitialConfigFatal(t *testing.T) {
	require.Equal(t, 1, run([]string{"--config", filepath.Join(t.TempDir(), "missing.yaml")}))
	path := filepath.Join(t.TempDir(), "invalid.yaml")
	require.NoError(t, os.WriteFile(path, []byte("unknown: invalid"), 0600))
	require.Equal(t, 1, run([]string{"--config", path}))
	require.Equal(t, 0, run([]string{"--version"}))
	require.Equal(t, 2, run([]string{"--not-a-flag"}))
}

type logCapture struct{ entries *[]string }

func (l logCapture) Init(logr.RuntimeInfo) {}
func (l logCapture) Enabled(int) bool      { return true }
func (l logCapture) Info(_ int, msg string, values ...any) {
	*l.entries = append(*l.entries, fmt.Sprint(msg, values))
}
func (l logCapture) Error(err error, msg string, values ...any) {
	*l.entries = append(*l.entries, fmt.Sprint(msg, err, values))
}
func (l logCapture) WithName(string) logr.LogSink   { return l }
func (l logCapture) WithValues(...any) logr.LogSink { return l }

const validConfig = `hubName: ocp4-a
installURL: https://example.invalid/install
activityQueue: activity
workflowQueue: workflow
segmentQueue: segment
pools: {p: {}}
`

func TestReloadAcceptedOnlyAndRestartDedup(t *testing.T) {
	initial, err := config.Load([]byte(validConfig))
	require.NoError(t, err)
	store := config.NewStore(initial)
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	reload := make(chan event.GenericEvent, 1)
	var entries []string
	callback := reloadHandler(initial.HubConfig, store, m, reload, logr.New(logCapture{entries: &entries}), "config.yaml")
	callback(errors.New("secret"))
	require.Empty(t, reload)
	require.NotContains(t, strings.Join(entries, " "), "secret")
	require.NoError(t, store.Update([]byte(strings.Replace(validConfig, "ocp4-a", "ocp4-b", 1))))
	callback(nil)
	require.Len(t, reload, 1)
	callback(nil)
	require.Len(t, reload, 1)
	<-reload
	require.NoError(t, store.Update([]byte(strings.Replace(validConfig, "pools: {p: {}}", "pools: {p: {}, q: {}}", 1)+"interval: 3m\n")))
	callback(nil)
	<-reload
	require.NoError(t, store.Update([]byte(strings.Replace(validConfig, "ocp4-a", "ocp4-b", 1))))
	callback(nil)
	<-reload
	require.NoError(t, store.Update([]byte(validConfig+"stuckAfter: 30m\n")))
	callback(nil)
	callback(nil)
	require.Len(t, reload, 1)
	require.Contains(t, strings.Join(entries, " "), "stuckAfter")

	restart := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry, "Hub settings changed") {
			restart++
		}
	}
	require.Equal(t, 3, restart)
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == "herzi_config_valid" {
			require.Equal(t, 1.0, family.Metric[0].Gauge.GetValue())
		}
	}
}
func TestWatcherEveryReplica(t *testing.T) {
	initial, err := config.Load([]byte(validConfig))
	require.NoError(t, err)
	store := config.NewStore(initial)
	reload := make(chan event.GenericEvent, 1)
	reg := prometheus.NewRegistry()
	path := filepath.Join(t.TempDir(), "config.yaml")
	watcher := &config.FileWatcher{Path: path, Store: store, OnUpdate: reloadHandler(initial.HubConfig, store, metrics.New(reg), reload, logr.Discard(), path)}
	require.False(t, watcher.NeedLeaderElection())
	require.NoError(t, os.WriteFile(path, []byte(validConfig), 0600))
	watcher.Poll()
	require.Len(t, reload, 1)
	<-reload
	require.NoError(t, os.WriteFile(path, []byte("invalid: secret"), 0600))
	watcher.Poll()
	require.Empty(t, reload)
	require.False(t, store.Valid())
	require.NoError(t, os.WriteFile(path, []byte(validConfig+"interval: 3m\n"), 0600))
	watcher.Poll()
	require.Len(t, reload, 1)
	require.True(t, store.Valid())
}

func TestFlagDefaultsAndTLS(t *testing.T) {
	o, err := parseLaunchOptions(nil)
	require.NoError(t, err)
	require.True(t, o.enableLeaderElection)
	require.True(t, o.secureMetrics)
	require.False(t, o.enableHTTP2)
	require.Equal(t, "/etc/herzi/config.yaml", o.configPath)
	opts := metricsOptions(o)
	require.NotNil(t, opts.FilterProvider)
	serverTLS := &tls.Config{NextProtos: []string{"h2"}}
	for _, apply := range opts.TLSOpts {
		apply(serverTLS)
	}
	require.Equal(t, []string{"http/1.1"}, serverTLS.NextProtos)
	o, err = parseLaunchOptions([]string{"--enable-http2", "--metrics-secure=false", "--metrics-cert-path=/certs", "--metrics-cert-name=cert.pem", "--metrics-cert-key=key.pem", "--kubeconfig=/tmp/test-kubeconfig"})
	require.NoError(t, err)
	opts = metricsOptions(o)
	require.Empty(t, opts.TLSOpts)
	require.Nil(t, opts.FilterProvider)
	require.Equal(t, "/certs", opts.CertDir)
	require.Equal(t, "cert.pem", opts.CertName)
	require.Equal(t, "key.pem", opts.KeyName)
}

func TestRemovedKeyReloadDiagnosticIsSafe(t *testing.T) {
	initial, err := config.Load([]byte(validConfig))
	require.NoError(t, err)
	store := config.NewStore(initial)
	reload := make(chan event.GenericEvent, 1)
	var entries []string
	handler := reloadHandler(initial.HubConfig, store, metrics.New(prometheus.NewRegistry()), reload, logr.New(logCapture{entries: &entries}), "config.yaml")
	for _, tt := range []struct{ suffix, want string }{
		{"defaults: {lowPct: secret}", "defaults.lowPct was removed: use highPct as the single target buffer percentage; delete lowPct"},
		{"defaults: {minBand: secret}", "defaults.minBand was removed: the controller no longer has a ceiling; delete minBand"},
		{"pools: {'secret\\nvalue': {lowPct: secret}}", "pools.<invalid>.lowPct was removed"},
	} {
		entries = nil
		err = store.Update([]byte(strings.Replace(validConfig, "pools: {p: {}}", "", 1) + tt.suffix))
		require.Error(t, err)
		handler(fmt.Errorf("secret wrapper: %w", err))
		require.Empty(t, reload)
		require.Contains(t, strings.Join(entries, " "), tt.want)
		require.NotContains(t, strings.Join(entries, " "), "secret")
		require.Equal(t, initial, store.Snapshot())
	}
	entries = nil
	handler(errors.New("decoder secret"))
	require.NotContains(t, strings.Join(entries, " "), "secret")
}

func TestMergedRemovedKeysStartupAndReload(t *testing.T) {
	initial, err := config.Load([]byte(validConfig))
	require.NoError(t, err)
	base := strings.Replace(validConfig, "pools: {p: {}}", "", 1)
	for _, tc := range []struct{ yaml, path, key string }{
		{"defaults:\n  lowPct: 0", "defaults", "lowPct"},
		{"defaults: {minBand: null}", "defaults", "minBand"},
		{"pools: {p: &old {minBand: 0}, q: *old}", "pools.p", "minBand"},
		{"defaults: {<<: {minBand: 0}}", "defaults", "minBand"},
		{"<<: {defaults: {lowPct: 0}}", "defaults", "lowPct"},
		{"pools: {<<: {p: {minBand: null}}}", "pools.p", "minBand"},
		{"<<: &root {pools: {p: {lowPct: null}}}", "pools.p", "lowPct"},
		{"<<: [{defaults: {minBand: null}}, {defaults: {highPct: 30}}]", "defaults", "minBand"},
		{"pools: {<<: [{p: {minBand: 0}}, {q: {}}]}", "pools.p", "minBand"},
		{"pools: {p: {<<: [{highPct: 30}, {minBand: 0}]}}", "pools.p", "minBand"},
		{"pools:\n  <<: &pools\n    p:\n      lowPct: 0", "pools.p", "lowPct"},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			want := tc.path + "." + tc.key + " was removed: the controller no longer has a ceiling; delete minBand"
			if tc.key == "lowPct" {
				want = tc.path + ".lowPct was removed: use highPct as the single target buffer percentage; delete lowPct"
			}
			store := config.NewStore(initial)
			var entries []string
			logger := logr.New(logCapture{entries: &entries})
			reload := make(chan event.GenericEvent, 1)
			handler := reloadHandler(initial.HubConfig, store, metrics.New(prometheus.NewRegistry()), reload, logger, "config.yaml")
			data := []byte(base + tc.yaml)
			err := store.Update(data)
			var removed *config.RemovedKeyError
			require.ErrorAs(t, err, &removed)
			require.EqualError(t, err, want)
			handler(fmt.Errorf("raw decoder secret: %w", err))
			require.Len(t, entries, 1)
			require.Contains(t, entries[0], want)
			require.NotContains(t, entries[0], "secret")
			require.NotContains(t, entries[0], "decode config")
			require.Empty(t, reload)
			require.Equal(t, initial, store.Snapshot())
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, data, 0600))
			previousLog := setupLog
			setupLog = logger
			defer func() { setupLog = previousLog }()
			entries = nil
			require.Equal(t, 1, run([]string{"--config", path}))
			require.Len(t, entries, 1)
			require.Contains(t, entries[0], want)
			require.Contains(t, entries[0], "The configuration file is invalid")
		})
	}
}
