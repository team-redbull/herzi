package installer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/team-redbull/herzi/internal/pipeline"
)

const removePath = "/workflows/server-lifecycle/remove-server-from-mce"

func newTestRemover(t *testing.T, server interface {
	URL() string
	Client() *http.Client
}, token string) *Remover {
	t.Helper()
	r, err := NewRemover(Options{URL: server.URL() + removePath, MCECluster: "ocp4-test", Namespace: "inventory",
		Timeout: time.Second, BearerToken: token, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type pipe struct {
	url    string
	client *http.Client
}

func (p pipe) URL() string          { return p.url }
func (p pipe) Client() *http.Client { return p.client }
func serve(t *testing.T, handler http.HandlerFunc) pipe {
	t.Helper()
	server := newPipeServer(t, false, handler)
	t.Cleanup(server.Close)
	return pipe{server.URL, server.Client()}
}

func TestRemoveSendsExactlyOneServer(t *testing.T) {
	var requests atomic.Int32
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body map[string]any
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &body); err != nil || r.Method != http.MethodPost || r.URL.Path != removePath {
			t.Errorf("request: %s %s %s", r.Method, r.URL.Path, data)
		}
		want := map[string]any{"server_name": "ocp-dell-X", "mce_cluster": "ocp4-test", "namespace": "inventory"}
		if len(body) != 3 || body["server_name"] != want["server_name"] || body["mce_cluster"] != want["mce_cluster"] || body["namespace"] != want["namespace"] {
			t.Errorf("body %s", data)
		}
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("headers")
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"workflow_id":"remove-server-from-mce-ocp-dell-x","run_id":"r1"}`)
	})
	got, err := newTestRemover(t, server, "tok").Remove(context.Background(), pipeline.RemoveRequest{Agent: "a", Server: "ocp-dell-X"})
	if err != nil || got.HTTPCode != 202 || got.AlreadyRunning || got.WorkflowID != "remove-server-from-mce-ocp-dell-x" || got.RunID != "r1" || requests.Load() != 1 {
		t.Fatalf("%+v %v requests=%d", got, err, requests.Load())
	}
}

func TestRemoveOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    int
		body    string
		class   error
		kind    string
		running bool
	}{
		{name: "already running", code: 409, body: `{"detail":"in flight"}`, running: true},
		{name: "rejected", code: 422, body: `{"detail":"bad"}`, class: pipeline.ErrRejected, kind: pipeline.KindRejected},
		{name: "server error", code: 500, body: "boom", class: pipeline.ErrAmbiguous, kind: pipeline.KindUnexpectedStatus},
		{name: "not json", code: 202, body: "ok", class: pipeline.ErrAmbiguous, kind: pipeline.KindInvalidResponse},
		{name: "no workflow id", code: 202, body: `{"run_id":"r"}`, class: pipeline.ErrAmbiguous, kind: pipeline.KindInvalidResponse},
		{name: "foreign workflow id", code: 202, body: `{"workflow_id":"remove-server-from-mce-other","run_id":"r"}`, class: pipeline.ErrAmbiguous, kind: pipeline.KindInvalidResponse},
		{name: "oversized", code: 202, body: strings.Repeat("x", maxBody+1), class: pipeline.ErrAmbiguous, kind: pipeline.KindResponseUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.body)
			})
			got, err := newTestRemover(t, server, "").Remove(context.Background(), pipeline.RemoveRequest{Server: "srv"})
			if got.HTTPCode != tc.code || got.AlreadyRunning != tc.running {
				t.Fatalf("%+v", got)
			}
			if tc.class == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var failure *pipeline.InstallError
			if !errors.Is(err, tc.class) || !errors.As(err, &failure) || failure.Kind != tc.kind {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestRemoveRejectsEmptyServerWithoutCalling(t *testing.T) {
	var requests atomic.Int32
	server := serve(t, func(http.ResponseWriter, *http.Request) { requests.Add(1) })
	_, err := newTestRemover(t, server, "").Remove(context.Background(), pipeline.RemoveRequest{Server: " "})
	if !errors.Is(err, pipeline.ErrRejected) || requests.Load() != 0 {
		t.Fatalf("%v requests=%d", err, requests.Load())
	}
}

func TestRemoveTimeoutIsAmbiguousAndHidesTheToken(t *testing.T) {
	release := make(chan struct{})
	server := serve(t, func(http.ResponseWriter, *http.Request) { <-release })
	defer close(release)
	r, err := NewRemover(Options{URL: server.URL() + removePath + "?token=secret", MCECluster: "ocp4-test", Namespace: "inventory",
		Timeout: 50 * time.Millisecond, BearerToken: "secret", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Remove(context.Background(), pipeline.RemoveRequest{Server: "srv"})
	var failure *pipeline.InstallError
	if !errors.Is(err, pipeline.ErrAmbiguous) || !errors.As(err, &failure) || failure.Kind != pipeline.KindTimeout {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error()+failure.URL, "secret") {
		t.Fatalf("leaked: %v %s", err, failure.URL)
	}
}

func TestNewRemoverValidates(t *testing.T) {
	if _, err := NewRemover(Options{URL: "relative", MCECluster: "m", Namespace: "n"}); err == nil || !strings.Contains(err.Error(), "remove client") {
		t.Fatal(err)
	}
}
