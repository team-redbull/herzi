package installer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/team-redbull/herzi/internal/pipeline"
)

func TestEveryRequestUsesAFreshConnection(t *testing.T) {
	var connections, closeHeaders atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Close {
			closeHeaders.Add(1)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"results":[{"slot":1,"status":"started"}]}`)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	i := newTestInstaller(t, server.URL, nil)
	for range 3 {
		if _, err := i.Install(context.Background(), pipeline.InstallRequest{Count: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if connections.Load() != 3 || closeHeaders.Load() != 3 {
		t.Fatalf("want 3 fresh connections announced with Connection: close, got %d connections and %d close headers",
			connections.Load(), closeHeaders.Load())
	}
}

func TestTransportFailureKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		kind string
	}{
		{"refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, pipeline.KindConnectionRefused},
		{"reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, pipeline.KindConnectionReset},
		{"closed early", io.EOF, pipeline.KindConnectionClosed},
		{"closed idle", errors.New("http: server closed idle connection"), pipeline.KindConnectionClosed},
		{"unreachable", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH}, pipeline.KindUnreachable},
		{"dns", &net.DNSError{Err: "no such host", Name: "api.example", IsNotFound: true}, pipeline.KindDNS},
		{"untrusted certificate", x509.UnknownAuthorityError{}, pipeline.KindTLSUntrusted},
		{"hostname", x509.HostnameError{Host: "api.example", Certificate: &x509.Certificate{}}, pipeline.KindTLSHostname},
		{"not a TLS server", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, pipeline.KindTLSNotTLS},
		{"other", errors.New("boom"), pipeline.KindTransport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, tc.err })}
			i := newTestInstaller(t, "http://example.test/route?token=abc", func(o *Options) { o.HTTPClient = client })
			_, err := i.Install(context.Background(), pipeline.InstallRequest{Count: 1})
			var failure *pipeline.InstallError
			if !errors.As(err, &failure) || !errors.Is(err, pipeline.ErrAmbiguous) || failure.Kind != tc.kind {
				t.Fatalf("want ambiguous %s, got %#v (%v)", tc.kind, failure, err)
			}
			if failure.URL != "http://example.test/route" || strings.Contains(err.Error(), "abc") {
				t.Fatalf("URL or detail leaks credentials or query: %q %q", failure.URL, failure.Detail)
			}
		})
	}
}

func TestHTTPFailureKindsCarryStatusAndBody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int
		body  string
		kind  string
		class error
	}{
		{"validation", 422, `{"detail":"infra_env is required"}`, pipeline.KindRejected, pipeline.ErrRejected},
		{"bad gateway", 502, `upstream unreachable`, pipeline.KindUnexpectedStatus, pipeline.ErrAmbiguous},
		{"redirect", 307, `moved`, pipeline.KindUnexpectedStatus, pipeline.ErrAmbiguous},
		{"garbage", 202, `<html>login</html>`, pipeline.KindInvalidResponse, pipeline.ErrAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newPipeServer(t, false, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			i := newTestInstaller(t, server.URL+"/install?secret=1", nil, server.Client())
			_, err := i.Install(context.Background(), pipeline.InstallRequest{Count: 1})
			var failure *pipeline.InstallError
			if !errors.As(err, &failure) || !errors.Is(err, tc.class) || failure.Kind != tc.kind ||
				failure.HTTP != tc.code || failure.Body != tc.body || strings.Contains(failure.URL, "secret") {
				t.Fatalf("got %#v (%v)", failure, err)
			}
		})
	}
}
