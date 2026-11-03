package controller

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/metrics"
	"github.com/team-redbull/herzi/internal/pipeline"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	keyQueue       = "queue"
	errorReminder  = 5 * time.Minute
	statusInterval = 10 * time.Minute
	detailLimit    = 600
)

type statusEntry struct {
	signature string
	at        time.Time
}

// blockLog is how one block reason is shown: its own message, whether it is an error, and whether it is hub-wide.
type blockLog struct {
	code, msg, detail string
	isError, hub      bool
	fields            []any
}

func describeBlock(reason string, hub config.HubConfig) blockLog {
	code := metrics.BlockReasons([]string{reason})[0]
	d := blockLog{code: code, detail: reason, isError: true}
	switch code {
	case "paused":
		d.msg, d.isError = "Pool is paused; no machines will be ordered", false
	case "blocked_until":
		d.msg, d.isError = "Pool is waiting after an uncertain install request; no machines will be ordered until then", false
		d.detail = strings.TrimPrefix(reason, "pool blocked until ")
	case "deleting":
		d.msg, d.isError = "InfraEnv is being deleted; no machines will be ordered", false
	case "early_bound":
		d.msg, d.isError = "InfraEnv is bound to a cluster (early binding); no machines will be ordered", false
	case "unmanaged":
		d.msg, d.isError = "InfraEnv is not managed", false
	case "sweep_error":
		d.msg = "Cannot read the install workflows from Temporal; ordering is blocked"
		d.detail = summarize(strings.TrimPrefix(reason, "pool sweep failed: "))
	case "read_error":
		d.msg = "Cannot read or parse a Kubernetes object; ordering is blocked"
		d.detail = summarize(reason)
	case "poller_error":
		d.msg, d.hub = "Cannot check the Temporal task queues; ordering is blocked", true
		d.detail = summarize(strings.TrimPrefix(reason, "poller check failed: "))
	case "activity_pollers":
		d.msg, d.hub = "No worker is polling the Temporal activity queue; ordering is blocked", true
		d.fields = []any{keyQueue, hub.ActivityQueue}
	case "workflow_pollers":
		d.msg, d.hub = "No worker is polling the Temporal workflow queue; ordering is blocked", true
		d.fields = []any{keyQueue, hub.WorkflowQueue}
	case "segment_pollers":
		d.msg, d.hub = "No worker is polling the Temporal segment queue; ordering is blocked", true
		d.fields = []any{keyQueue, hub.SegmentQueue}
	case "selector":
		d.msg = "A NodePool selector must be only the InfraEnv label; ordering is blocked"
	case "replicas":
		d.msg = "A NodePool has no replicas (autoscaling?); ordering is blocked"
	case "config_invalid":
		d.msg = "The pool configuration is invalid; ordering is blocked"
	default:
		d.msg = "Pool is blocked"
	}
	return d
}

var (
	urlUserinfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s@]*@`)
	urlQuery    = regexp.MustCompile(`\?[^\s"'<>)]*`)
	bearerToken = regexp.MustCompile(`(?i)(bearer\s+)\S+`)
)

// sanitize removes credentials that can ride along in external error text: URL userinfo, query strings
// (tokens are often passed there) and bearer tokens.
func sanitize(text string) string {
	text = urlUserinfo.ReplaceAllString(text, "${1}[redacted]@")
	text = urlQuery.ReplaceAllString(text, "?[redacted]")
	return bearerToken.ReplaceAllString(text, "${1}[redacted]")
}

// summarize keeps the first error of a joined error (one line per failing slot) and counts the rest.
func summarize(text string) string {
	lines := strings.Split(strings.TrimSpace(sanitize(text)), "\n")
	out := lines[0]
	if len(lines) > 1 {
		out += fmt.Sprintf(" (+%d more errors)", len(lines)-1)
	}
	if len(out) > detailLimit {
		out = out[:detailLimit] + "..."
	}
	return out
}

// blocked records why a pool is blocked, logs each reason once with its own message (errors again every
// errorReminder while they last) and says so when the pool recovers.
func (r *Reconciler) blocked(ctx context.Context, pool string, reasons []string) {
	r.blockedAt(ctx, pool, reasons, r.Clock())
}

func (r *Reconciler) blockedAt(ctx context.Context, pool string, reasons []string, now time.Time) {
	reason := strings.Join(metrics.BlockReasons(reasons), "; ")
	r.mu.Lock()
	old := r.reasons[pool]
	if reason == "" {
		delete(r.reasons, pool)
		r.clearLogged(pool)
	} else {
		r.reasons[pool] = reason
	}
	r.mu.Unlock()
	logger := log.FromContext(ctx)
	if reason == "" {
		if old != "" {
			logger.Info("Pool is no longer blocked", "pool", pool, "was", old)
		}
		return
	}
	for _, text := range reasons {
		r.logBlock(logger, pool, describeBlock(text, r.startup), now)
	}
}

func (r *Reconciler) logBlock(logger logr.Logger, pool string, d blockLog, now time.Time) {
	key := pool + "|" + d.code + "|" + d.detail
	if d.hub {
		key = "\x00hub|" + d.code + "|" + d.detail
	}
	r.mu.Lock()
	last, seen := r.logged[key]
	due := !seen || (d.isError && now.Sub(last) >= errorReminder)
	if due {
		r.logged[key] = now
	}
	for k, at := range r.logged {
		if now.Sub(at) > time.Hour {
			delete(r.logged, k)
		}
	}
	r.mu.Unlock()
	if !due {
		return
	}
	fields := []any{"reason", d.code}
	if !d.hub {
		fields = append(fields, "pool", pool)
	}
	fields = append(fields, d.fields...)
	if d.isError {
		logger.Error(errors.New(d.detail), d.msg, fields...)
		return
	}
	logger.Info(d.msg, append(fields, "detail", d.detail)...)
}

func (r *Reconciler) clearLogged(pool string) {
	for k := range r.logged {
		if strings.HasPrefix(k, pool+"|") {
			delete(r.logged, k)
		}
	}
}

// logStatus prints the numbers behind a pool's decision when they change, and every statusInterval otherwise.
func (r *Reconciler) logStatus(ctx context.Context, f pipeline.PoolFacts, v pipeline.Verdict,
	i pipeline.Intent, p config.PoolConfig, now time.Time) {
	buffer, target, _ := pipeline.Target(f.Demand, p)
	supply := f.Assigned + f.Claimable + f.Settling + len(f.Open)
	decision, toOrder := "hold", 0
	switch x := i.(type) {
	case pipeline.Hold:
		decision = "hold: " + x.Reason
	case pipeline.Grow:
		for _, req := range pipeline.Plan(x, p, f) {
			toOrder += req.Count
		}
		mode := "full"
		if x.Mode == pipeline.Probe {
			mode = "probe, one machine to test capacity"
		}
		decision = fmt.Sprintf("order %d (%s)", toOrder, mode)
	}
	if v.Blocked {
		decision = "blocked: " + strings.Join(metrics.BlockReasons(v.Reasons), ", ")
		toOrder = 0
	}
	nodePools := make([]string, 0, len(f.NodePools))
	for _, np := range f.NodePools {
		nodePools = append(nodePools, fmt.Sprintf("%s=%d", np.Name, np.Replicas))
	}
	var oldest time.Duration
	for _, run := range f.Open {
		oldest = max(oldest, now.Sub(run.Started))
	}
	shortfall := max(0, target-supply)
	signature := fmt.Sprint(nodePools, f.Demand, f.Assigned, f.Claimable, f.Settling, f.StuckAgents, len(f.Open),
		buffer, target, shortfall, toOrder, decision, p.DryRunGrow)
	name := string(f.Key)
	r.mu.Lock()
	last, seen := r.status[name]
	due := !seen || last.signature != signature || now.Sub(last.at) >= statusInterval
	if due {
		r.status[name] = statusEntry{signature: signature, at: now}
	}
	r.mu.Unlock()
	if !due {
		return
	}
	log.FromContext(ctx).Info("Pool status", "pool", name,
		"nodePools", nodePools, "demand", f.Demand,
		"assigned", f.Assigned, "claimable", f.Claimable, "settling", f.Settling, "stuckAgents", f.StuckAgents,
		"openRuns", len(f.Open), "oldestOpenRun", oldest.Round(time.Second).String(),
		"buffer", buffer, "target", target, "supply", supply, "shortfall", shortfall, "toOrder", toOrder,
		"decision", decision, "dryRun", p.DryRunGrow)
}

var installMessages = map[string]string{
	pipeline.KindTimeout:            "Install request timed out; machines may have started",
	pipeline.KindCanceled:           "Install request was canceled; machines may have started",
	pipeline.KindDNS:                "Install request failed: the install URL host name cannot be resolved",
	pipeline.KindConnectionRefused:  "Install request failed: the install endpoint refused the connection",
	pipeline.KindConnectionReset:    "Install request failed: the connection to the install endpoint was reset",
	pipeline.KindConnectionClosed:   "Install request failed: the install endpoint closed the connection without replying",
	pipeline.KindUnreachable:        "Install request failed: the install endpoint is unreachable from this pod",
	pipeline.KindTLSUntrusted:       "Install request failed: the install endpoint's TLS certificate is not trusted by this pod (its CA is missing)",
	pipeline.KindTLSHostname:        "Install request failed: the install endpoint's TLS certificate does not match its host name",
	pipeline.KindTLSInvalid:         "Install request failed: the install endpoint's TLS certificate is expired or not valid",
	pipeline.KindTLSNotTLS:          "Install request failed: the install URL is https but the server does not speak TLS",
	pipeline.KindTLSOther:           "Install request failed: TLS handshake error",
	pipeline.KindTransport:          "Install request failed: network error",
	pipeline.KindRejected:           "Install request rejected by the API; nothing started",
	pipeline.KindUnexpectedStatus:   "Install request got an unexpected HTTP status (expected 200 or 202); machines may have started",
	pipeline.KindInvalidResponse:    "Install request got a reply that is not a valid result list; machines may have started",
	pipeline.KindResponseUnreadable: "Install request reply could not be read; machines may have started",
}

// recordInstall counts the attempt and logs it: an error with its own message per failure kind, or the accepted report.
func (r *Reconciler) recordInstall(ctx context.Context, pool string, request pipeline.InstallRequest,
	res pipeline.InstallResult, err error, retryAt time.Time) {
	kind := ""
	var failure *pipeline.InstallError
	switch {
	case errors.As(err, &failure):
		kind = failure.Kind
	case err != nil:
		kind = "other"
	}
	r.Metrics.InstallResponse(pool, res.HTTPCode, kind, res.Duration)
	logger := log.FromContext(ctx)
	took := res.Duration.Round(time.Millisecond).String()
	if err != nil {
		fields := []any{"pool", pool, "kind", kind, "count", request.Count, "took", took}
		if failure != nil {
			fields = append(fields, "url", failure.URL, "detail", failure.Detail)
			if failure.HTTP != 0 {
				fields = append(fields, "httpStatus", failure.HTTP)
			}
			if failure.Body != "" {
				fields = append(fields, "responseBody", failure.Body)
			}
		}
		if errors.Is(err, pipeline.ErrAmbiguous) {
			fields = append(fields, "outcome", "unknown, machines may have started", "blockedUntil", retryAt.Format(time.RFC3339))
		}
		msg, ok := installMessages[kind]
		if !ok {
			msg = "Install request failed"
		}
		logged := error(failure)
		if failure == nil {
			logged = pipeline.ErrAmbiguous
			if errors.Is(err, pipeline.ErrRejected) {
				logged = pipeline.ErrRejected
			}
		}
		logger.Error(logged, msg, fields...)
		return
	}
	fields := []any{"pool", pool, "requested", res.Requested, "started", res.Started, "httpStatus", res.HTTPCode, "took", took}
	if len(res.Busy) > 0 {
		fields = append(fields, "alreadyRunning", res.Busy)
	}
	if len(res.Failed) > 0 {
		fields = append(fields, "failedSlots", res.Failed)
		logger.Error(errors.New("the API reported failed slots"), "Install request accepted but some machines failed to start", fields...)
		return
	}
	logger.Info("Install request accepted", fields...)
}

// classifyTemporal names a Temporal read failure for the herzi_temporal_errors_total metric.
func classifyTemporal(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded:
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled:
		return "canceled"
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "x509:"):
		return "tls_certificate"
	case strings.Contains(text, "tls:"):
		return "tls"
	}
	switch status.Code(err) {
	case codes.Unavailable:
		return "unavailable"
	case codes.Unauthenticated:
		return "unauthenticated"
	case codes.PermissionDenied:
		return "permission_denied"
	case codes.NotFound:
		return "not_found"
	case codes.ResourceExhausted:
		return "resource_exhausted"
	case codes.OK, codes.Unknown:
		return "other"
	}
	return "grpc_error"
}
