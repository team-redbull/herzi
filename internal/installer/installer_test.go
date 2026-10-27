package installer

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/team-redbull/herzi/internal/pipeline"
)

const mixedReport = `{"started":2,"already_running":1,"failed":1,"results":[{"slot":1,"workflow_id":"busy","status":"already_running"},{"slot":2,"status":"started","run_id":"r"},{"slot":3,"status":"started"},{"slot":4,"status":"failed","error":"ack lost"}]}`

func newTestInstaller(t *testing.T, endpoint string, change func(*Options), clients ...*http.Client) *Installer {
	t.Helper()
	o := Options{URL: endpoint, MCECluster: "ocp4-test", Namespace: "inventory", Timeout: time.Second}
	if len(clients) > 0 {
		o.HTTPClient = clients[0]
	}
	if change != nil {
		change(&o)
	}
	i, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return i
}
func TestSuccessAndExactRequest(t *testing.T) {
	golden, err := os.ReadFile("testdata/request.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{200, 202} {
		for _, token := range []string{"", "secret-value"} {
			t.Run(http.StatusText(code)+token, func(t *testing.T) {
				var requests atomic.Int32
				server := newPipeServer(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					body, _ := io.ReadAll(r.Body)
					if string(body) != strings.TrimSpace(string(golden)) || r.Method != "POST" || r.URL.Path != "/workflows/server-lifecycle/install-server" {
						t.Errorf("request: %s %s %s", r.Method, r.URL.Path, body)
					}
					if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" || !strings.HasPrefix(r.Header.Get("User-Agent"), "herzi/") {
						t.Error("incorrect headers")
					}
					want := ""
					if token != "" {
						want = "Bearer " + token
					}
					if r.Header.Get("Authorization") != want {
						t.Error("authorization mismatch")
					}
					w.WriteHeader(code)
					_, _ = io.WriteString(w, mixedReport)
				}))
				defer server.Close()
				i := newTestInstaller(t, server.URL+"/workflows/server-lifecycle/install-server", func(o *Options) { o.BearerToken = token }, server.Client())
				got, err := i.Install(context.Background(), pipeline.InstallRequest{Key: "pool-a", Count: 2})
				want := pipeline.InstallResult{Requested: 2, Started: []int{2, 3}, Busy: []int{1}, Failed: []int{4}, HTTPCode: code}
				if err != nil || !reflect.DeepEqual(got, want) || requests.Load() != 1 {
					t.Fatalf("got %+v %v requests=%d", got, err, requests.Load())
				}
			})
		}
	}
}
func TestLocalCountRejection(t *testing.T) {
	var requests atomic.Int32
	server := newPipeServer(t, false, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	i := newTestInstaller(t, server.URL, nil, server.Client())
	for _, count := range []int{-1, 0, 21} {
		result, err := i.Install(context.Background(), pipeline.InstallRequest{Count: count})
		if !errors.Is(err, pipeline.ErrRejected) || result.Requested != count || result.HTTPCode != 0 {
			t.Fatal(result, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("sent invalid count")
	}
}
func TestHTTPClassification(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		body     string
		sentinel error
		started  []int
	}{
		{"validation", 422, `{"detail":"invalid request"}`, pipeline.ErrRejected, nil},
		{"unauthorized", 401, `{"detail":"denied"}`, pipeline.ErrRejected, nil},
		{"500 partial", 500, mixedReport, pipeline.ErrAmbiguous, []int{2, 3}},
		{"502", 502, `bad gateway`, pipeline.ErrAmbiguous, nil},
		{"503", 503, `unavailable`, pipeline.ErrAmbiguous, nil},
		{"504", 504, `timeout`, pipeline.ErrAmbiguous, nil},
		{"garbage", 202, `garbage`, pipeline.ErrAmbiguous, nil},
		{"array body", 202, `[]`, pipeline.ErrAmbiguous, nil},
		{"truncated report", 202, `{"results":[{"slot":1,"status":"started"},{"slot":`, pipeline.ErrAmbiguous, []int{1}},
		{"truncated array end", 202, `{"results":[{"slot":1,"status":"started"}`, pipeline.ErrAmbiguous, []int{1}},
		{"truncated object end", 202, `{"results":[]`, pipeline.ErrAmbiguous, nil},
		{"missing value", 202, `{"results":`, pipeline.ErrAmbiguous, nil},
		{"bad unused field", 202, `{"started":`, pipeline.ErrAmbiguous, nil},
		{"duplicate results", 202, `{"results":[],"results":[]}`, pipeline.ErrAmbiguous, nil},
		{"missing results", 200, `{}`, pipeline.ErrAmbiguous, nil},
		{"null results", 202, `{"results":null}`, pipeline.ErrAmbiguous, nil},
		{"empty batch", 202, `{"results":[]}`, nil, nil},
		{"unknown status partial", 202, `{"results":[{"slot":1,"status":"started"},{"slot":2,"status":"unexpected"}]}`, pipeline.ErrAmbiguous, []int{1}},
		{"bad slot partial", 202, `{"results":[{"slot":1,"status":"started"},{"slot":null,"status":"started"}]}`, pipeline.ErrAmbiguous, []int{1}},
		{"out of range", 202, `{"results":[{"slot":101,"status":"started"}]}`, pipeline.ErrAmbiguous, nil},
		{"duplicate", 202, `{"results":[{"slot":1,"status":"started"},{"slot":1,"status":"started"}]}`, pipeline.ErrAmbiguous, []int{1}},
		{"bad item", 202, `{"results":[{"slot":"one","status":"started"}]}`, pipeline.ErrAmbiguous, nil},
		{"trailing garbage", 202, mixedReport + `bad`, pipeline.ErrAmbiguous, []int{2, 3}},
		{"too large", 202, mixedReport + strings.Repeat(" ", maxBody), pipeline.ErrAmbiguous, []int{2, 3}},
		{"redirect", 307, mixedReport, pipeline.ErrAmbiguous, []int{2, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := newPipeServer(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			i := newTestInstaller(t, server.URL, nil, server.Client())
			result, err := i.Install(context.Background(), pipeline.InstallRequest{Count: 2})
			if (tc.sentinel == nil && err != nil) || (tc.sentinel != nil && !errors.Is(err, tc.sentinel)) || result.HTTPCode != tc.code || !reflect.DeepEqual(result.Started, tc.started) || calls.Load() != 1 {
				t.Fatalf("%+v %v calls=%d", result, err, calls.Load())
			}
			if tc.code == 422 && !strings.Contains(err.Error(), "invalid request") {
				t.Fatal(err)
			}
		})
	}
}
func TestRejectDetailLimitAndRedaction(t *testing.T) {
	token := "do-not-expose"
	server := newPipeServer(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = io.WriteString(w, token+strings.Repeat("x", 1000))
	}))
	defer server.Close()
	i := newTestInstaller(t, server.URL, func(o *Options) { o.BearerToken = token }, server.Client())
	_, err := i.Install(context.Background(), pipeline.InstallRequest{Count: 1})
	prefix := pipeline.ErrRejected.Error() + ": HTTP 422: "
	if !errors.Is(err, pipeline.ErrRejected) || strings.Contains(err.Error(), token) || len(strings.TrimPrefix(err.Error(), prefix)) != 512 {
		t.Fatal(err)
	}
}
func TestTimeoutAndCancellation(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancelEarly], func(t *testing.T) {
			var calls atomic.Int32
			server := newPipeServer(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case <-r.Context().Done():
				case <-time.After(200 * time.Millisecond):
				}
			}))
			defer server.Close()
			i := newTestInstaller(t, server.URL, func(o *Options) { o.Timeout = 20 * time.Millisecond }, server.Client())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelEarly {
				cancel()
			}
			_, err := i.Install(ctx, pipeline.InstallRequest{Count: 1})
			sentinel := context.DeadlineExceeded
			if cancelEarly {
				sentinel = context.Canceled
			}
			if !errors.Is(err, pipeline.ErrAmbiguous) || !errors.Is(err, sentinel) || calls.Load() > 1 {
				t.Fatal(err)
			}
		})
	}
}
func TestConnectionRefused(t *testing.T) {
	client := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	}}}
	i := newTestInstaller(t, "http://127.0.0.1:1", func(o *Options) { o.HTTPClient = client })
	_, err := i.Install(context.Background(), pipeline.InstallRequest{Count: 1})
	if !errors.Is(err, pipeline.ErrAmbiguous) {
		t.Fatal(err)
	}
}
func TestTLSFailure(t *testing.T) {
	server := newPipeServer(t, true, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	client := *server.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = nil
	client.Transport = transport
	i := newTestInstaller(t, server.URL, func(o *Options) { o.HTTPClient = &client })
	_, err := i.Install(context.Background(), pipeline.InstallRequest{Count: 1})
	if !errors.Is(err, pipeline.ErrAmbiguous) {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenBody struct{ closed bool }

func (*brokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (b *brokenBody) Close() error           { b.closed = true; return nil }
func TestBodyReadFailureClosesBody(t *testing.T) {
	b := &brokenBody{}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 202, Header: make(http.Header), Body: b}, nil
	})}
	i := newTestInstaller(t, "http://example.test", func(o *Options) { o.HTTPClient = client })
	_, err := i.Install(context.Background(), pipeline.InstallRequest{Count: 1})
	if !errors.Is(err, pipeline.ErrAmbiguous) || !b.closed {
		t.Fatal(err, b.closed)
	}
}
func TestTransportErrorDoesNotLeakSecrets(t *testing.T) {
	token := "secret"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New(token) })}
	i := newTestInstaller(t, "http://example.test", func(o *Options) { o.HTTPClient = client; o.BearerToken = token })
	_, err := i.Install(context.Background(), pipeline.InstallRequest{Count: 1})
	if !errors.Is(err, pipeline.ErrAmbiguous) || strings.Contains(err.Error(), token) {
		t.Fatal(err)
	}
}
func TestOptions(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Options)
	}{
		{"empty URL", func(o *Options) { o.URL = "" }}, {"bad URL", func(o *Options) { o.URL = "://" }}, {"relative", func(o *Options) { o.URL = "/install" }}, {"scheme", func(o *Options) { o.URL = "ftp://example.test" }},
		{"credentials", func(o *Options) { o.URL = "https://user:secret@example.test" }}, {"fragment", func(o *Options) { o.URL += "#fragment" }},
		{"MCE", func(o *Options) { o.MCECluster = "" }}, {"namespace", func(o *Options) { o.Namespace = " " }}, {"timeout", func(o *Options) { o.Timeout = -time.Second }}, {"token", func(o *Options) { o.BearerToken = "secret\r\nheader" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := Options{URL: "http://example.test", MCECluster: "mce", Namespace: "inventory"}
			tc.change(&o)
			_, err := New(o)
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal(err)
			}
		})
	}
	client := &http.Client{Timeout: time.Second}
	i := newTestInstaller(t, "http://example.test", func(o *Options) { o.Timeout = 0; o.HTTPClient = client })
	if i.options.Timeout <= 0 || i.client == client || client.CheckRedirect != nil {
		t.Fatal("invalid defaults or modified caller's client")
	}
}

// In-memory listeners preserve HTTP/TLS semantics without binding TCP sockets.
type pipeListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.connections:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error { l.once.Do(func() { close(l.done) }); return nil }
func (*pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345} }
func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.connections <- server:
		return client, nil
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		return nil, ctx.Err()
	case <-l.done:
		_ = client.Close()
		_ = server.Close()
		return nil, net.ErrClosed
	}
}
func newPipeServer(t *testing.T, tls bool, handler http.Handler) *httptest.Server {
	t.Helper()
	listener := &pipeListener{connections: make(chan net.Conn), done: make(chan struct{})}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	if tls {
		server.StartTLS()
	} else {
		server.Start()
	}
	transport := &http.Transport{DialContext: listener.dial}
	if original, ok := server.Client().Transport.(*http.Transport); ok {
		transport.TLSClientConfig = original.TLSClientConfig
	}
	server.Client().Transport = transport
	t.Cleanup(transport.CloseIdleConnections)
	return server
}
