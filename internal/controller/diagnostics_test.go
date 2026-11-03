package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/pipeline"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type logRun struct {
	f       *fixture
	entries []logEntry
	ctx     context.Context
}

func newLogRun(t *testing.T, cfg config.Config, objects ...client.Object) *logRun {
	t.Helper()
	run := &logRun{f: setup(t, cfg, objects...)}
	run.ctx = log.IntoContext(context.Background(), logr.New(captureSink{entries: &run.entries}))
	return run
}

func (l *logRun) reconcile(t *testing.T, pool string) {
	t.Helper()
	_, err := l.f.r.Reconcile(l.ctx, ctrl.Request{Namespace: "inventory", Name: pool})
	require.NoError(t, err)
}

func (l *logRun) find(msgPart string) []logEntry {
	var out []logEntry
	for _, e := range l.entries {
		if strings.Contains(e.msg, msgPart) {
			out = append(out, e)
		}
	}
	return out
}

func field(e logEntry, key string) any {
	for i := 0; i+1 < len(e.values); i += 2 {
		if e.values[i] == key {
			return e.values[i+1]
		}
	}
	return nil
}

func TestStatusLineShowsTheNumbersBehindTheDecision(t *testing.T) {
	cfg := testConfig()
	run := newLogRun(t, cfg, infra("p"), node("workers", "p", 3), node("infra", "p", 2), agent("a", "p"))
	run.reconcile(t, "p")
	lines := run.find("Pool status")
	require.Len(t, lines, 1)
	e := lines[0]
	require.Equal(t, 5, field(e, "demand"), "total replicas over all NodePools")
	require.Equal(t, []string{"tenant/infra=2", "tenant/workers=3"}, field(e, "nodePools"))
	require.Equal(t, 2, field(e, "buffer"))
	require.Equal(t, 7, field(e, "target"), "demand plus buffer")
	require.Equal(t, 1, field(e, "claimable"))
	require.Equal(t, 0, field(e, "openRuns"))
	require.Equal(t, 6, field(e, "shortfall"))
	require.Equal(t, 6, field(e, "toOrder"))
	require.Equal(t, "order 6 (full)", field(e, "decision"))
}

func TestStatusLineIsQuietUntilSomethingChanges(t *testing.T) {
	run := newLogRun(t, testConfig(), infra("p"), node("workers", "p", 5))
	for range 4 {
		run.reconcile(t, "p")
	}
	// The fake installer starts the machines but the sweep fake reports no runs, so the numbers repeat.
	require.Len(t, run.find("Pool status"), 1, "unchanged numbers are not logged again")
	run.f.now = run.f.now.Add(11 * time.Minute)
	run.reconcile(t, "p")
	require.Len(t, run.find("Pool status"), 2, "a heartbeat after ten minutes")
}

func TestInstallFailuresAreErrorsWithTheirOwnMessageAndMetric(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure *pipeline.InstallError
		message string
	}{
		{"certificate", &pipeline.InstallError{Kind: pipeline.KindTLSUntrusted, Class: pipeline.ErrAmbiguous,
			Detail: "x509: certificate signed by unknown authority", URL: "https://api.example/install"}, "TLS certificate is not trusted"},
		{"timeout", &pipeline.InstallError{Kind: pipeline.KindTimeout, Class: pipeline.ErrAmbiguous,
			Detail: "context deadline exceeded", Cause: context.DeadlineExceeded}, "timed out"},
		{"status", &pipeline.InstallError{Kind: pipeline.KindUnexpectedStatus, Class: pipeline.ErrAmbiguous, HTTP: 503,
			Detail: "unexpected HTTP 503", Body: "upstream unavailable"}, "unexpected HTTP status"},
		{"rejected", &pipeline.InstallError{Kind: pipeline.KindRejected, Class: pipeline.ErrRejected, HTTP: 422,
			Detail: "HTTP 422: count too big", Body: "count too big"}, "rejected by the API"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			run := newLogRun(t, cfg, infra("p"), node("workers", "p", 5))
			run.f.install.err = tc.failure
			run.f.install.result = &pipeline.InstallResult{Requested: 7, HTTPCode: tc.failure.HTTP, Duration: 1500 * time.Millisecond}
			run.reconcile(t, "p")
			failures := run.find(tc.message)
			require.Len(t, failures, 1)
			e := failures[0]
			require.Error(t, e.err, "logged at error level")
			require.Equal(t, tc.failure.Kind, field(e, "kind"))
			require.Equal(t, "1.5s", field(e, "took"))
			if tc.failure.HTTP != 0 {
				require.Equal(t, tc.failure.HTTP, field(e, "httpStatus"))
				require.Equal(t, tc.failure.Body, field(e, "responseBody"))
			}
			require.Equal(t, 1.0, metric(t, run.f.reg, "herzi_install_errors_total", map[string]string{"pool": "p", "kind": tc.failure.Kind}))
			code := "none"
			if tc.failure.HTTP != 0 {
				code = fmt.Sprint(tc.failure.HTTP)
			}
			require.Equal(t, 1.0, metric(t, run.f.reg, "herzi_install_http_responses_total", map[string]string{"code": code}))
		})
	}
}

func TestAcceptedInstallIsLoggedAtInfoWithTheSlots(t *testing.T) {
	run := newLogRun(t, testConfig(), infra("p"), node("workers", "p", 5))
	run.f.install.result = &pipeline.InstallResult{Requested: 7, Started: []int{1, 2, 3, 4, 5, 6}, Busy: []int{7}, HTTPCode: 202}
	run.reconcile(t, "p")
	accepted := run.find("Install request accepted")
	require.Len(t, accepted, 1)
	require.NoError(t, accepted[0].err)
	require.Equal(t, []int{7}, field(accepted[0], "alreadyRunning"))
	require.Equal(t, 202, field(accepted[0], "httpStatus"))
	require.Equal(t, 0.0, metric(t, run.f.reg, "herzi_install_http_responses_total", map[string]string{"code": "none"}))
}

func TestBlockedPoolShowsTheRealErrorAndRepeatsEveryFiveMinutes(t *testing.T) {
	run := newLogRun(t, testConfig(), infra("p"), node("workers", "p", 5))
	run.f.sweep.result.Err = fmt.Errorf("describe slot install-server-ocp4-x_p_1: %w", status.Error(codes.Unavailable, "connection refused"))
	for range 3 {
		run.reconcile(t, "p")
	}
	errs := run.find("Cannot read the install workflows from Temporal")
	require.Len(t, errs, 1, "once, not on every reconcile")
	require.Error(t, errs[0].err)
	require.Contains(t, errs[0].err.Error(), "connection refused", "the real cause, not a label")
	require.Equal(t, "sweep_error", field(errs[0], "reason"))

	run.f.now = run.f.now.Add(4 * time.Minute)
	run.reconcile(t, "p")
	require.Len(t, run.find("Cannot read the install workflows from Temporal"), 1)
	run.f.now = run.f.now.Add(2 * time.Minute)
	run.reconcile(t, "p")
	require.Len(t, run.find("Cannot read the install workflows from Temporal"), 2, "reminder after five minutes")

	run.f.sweep.result.Err = nil
	run.reconcile(t, "p")
	require.Len(t, run.find("Pool is no longer blocked"), 1)
	require.Equal(t, 5.0, metric(t, run.f.reg, "herzi_temporal_errors_total", map[string]string{"op": "sweep", "kind": "unavailable"}),
		"one count per failed read, even though the log line is deduplicated")
}

func TestHubWideTemporalProblemIsLoggedOnceForAllPools(t *testing.T) {
	cfg := testConfig()
	cfg.ActivityQueue = "activity-queue"
	run := newLogRun(t, cfg, infra("p"), infra("q"))
	run.f.queues.result = pipeline.PollerStatus{Err: status.Error(codes.DeadlineExceeded, "context deadline exceeded")}
	run.reconcile(t, "p")
	run.reconcile(t, "q")
	require.Len(t, run.find("Cannot check the Temporal task queues"), 1, "a hub-wide problem is not repeated per pool")
	require.Len(t, run.find("No worker is polling the Temporal activity queue"), 1)
	require.Equal(t, "activity-queue", field(run.find("activity queue")[0], "queue"), "the log names the queue to check")
	require.Equal(t, 1.0, metric(t, run.f.reg, "herzi_temporal_errors_total", map[string]string{"op": "pollers", "kind": "deadline_exceeded"}))
}

func TestIgnoreLabelNameChangeIsLive(t *testing.T) {
	cfg := testConfig()
	ie := infra("p")
	ie.SetLabels(map[string]string{"example.com/skip": ""})
	run := newLogRun(t, cfg, ie, node("workers", "p", 5))
	require.NoError(t, run.f.r.Config.Update([]byte(reloadConfig+"ignoreLabel: example.com/skip\n")))
	run.reconcile(t, "p")
	require.Empty(t, run.f.install.requests, "the new label name applies without a restart")
}

func TestClassifyTemporal(t *testing.T) {
	for want, err := range map[string]error{
		"deadline_exceeded": context.DeadlineExceeded,
		"canceled":          context.Canceled,
		"unavailable":       status.Error(codes.Unavailable, "down"),
		"unauthenticated":   status.Error(codes.Unauthenticated, "no key"),
		"permission_denied": status.Error(codes.PermissionDenied, "no"),
		"not_found":         status.Error(codes.NotFound, "namespace"),
		"tls_certificate":   fmt.Errorf("connection error: x509: certificate signed by unknown authority"),
		"other":             fmt.Errorf("boom"),
	} {
		require.Equal(t, want, classifyTemporal(err), want)
	}
}
