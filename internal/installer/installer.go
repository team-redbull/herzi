// Package installer sends additive install requests without automatic retries.
package installer

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/team-redbull/herzi/internal/pipeline"
)

const maxBody = 1 << 20

// Options.URL must be the complete install route URL.
type Options struct {
	URL, MCECluster, Namespace string
	HTTPClient                 *http.Client
	Timeout                    time.Duration
	BearerToken                string
	// CAFile is a PEM bundle trusted in addition to the system roots, for an install endpoint signed by a private CA.
	// It applies to the default transport only; a supplied HTTPClient keeps its own TLS settings.
	CAFile string
}

type Installer struct {
	options   Options
	client    *http.Client
	userAgent string
}

var _ pipeline.Installer = (*Installer)(nil)

// New copies the supplied HTTP client and disables redirects to prevent POST replay or credential forwarding.
func New(o Options) (*Installer, error) {
	u, err := url.Parse(o.URL)
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("install URL must be an absolute HTTP(S) URL without credentials or fragment")
	}
	if strings.TrimSpace(o.MCECluster) == "" || strings.TrimSpace(o.Namespace) == "" {
		return nil, fmt.Errorf("MCECluster and Namespace are required")
	}
	if strings.ContainsAny(o.BearerToken, "\r\n") {
		return nil, fmt.Errorf("bearer token contains invalid header characters")
	}
	if o.Timeout < 0 {
		return nil, errors.New("timeout must not be negative")
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	client := http.Client{}
	if o.HTTPClient != nil {
		client = *o.HTTPClient
	} else {
		// A POST is never retried and a reused keep-alive connection that the router or a firewall already
		// dropped fails it at once, so every request gets a fresh connection, as curl does.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DisableKeepAlives = true
		if o.CAFile != "" {
			roots, err := rootsWith(o.CAFile)
			if err != nil {
				return nil, err
			}
			transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		}
		client.Transport = transport
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	version := "dev"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}
	return &Installer{options: o, client: &client, userAgent: "herzi/" + version}, nil
}

// rootsWith returns the system roots plus every certificate in the PEM file.
func rootsWith(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read install CA file: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("install CA file %q holds no certificate", path)
	}
	return roots, nil
}

type requestBody struct {
	InfraEnv   string `json:"infra_env"`
	MCECluster string `json:"mce_cluster"`
	Namespace  string `json:"namespace"`
	Count      int    `json:"count"`
}

// Install never retries; ErrAmbiguous means machines may have started and requires a fresh sweep.
func (i *Installer) Install(ctx context.Context, req pipeline.InstallRequest) (pipeline.InstallResult, error) {
	started := time.Now()
	result, err := i.install(ctx, req)
	result.Duration = time.Since(started)
	return result, err
}

func (i *Installer) install(ctx context.Context, req pipeline.InstallRequest) (pipeline.InstallResult, error) {
	result := pipeline.InstallResult{Requested: req.Count}
	if req.Count < 1 || req.Count > pipeline.APIMaxCount {
		return result, fmt.Errorf("%w: count must be 1..%d", pipeline.ErrRejected, pipeline.APIMaxCount)
	}
	body, err := json.Marshal(requestBody{string(req.Key), i.options.MCECluster, i.options.Namespace, req.Count})
	if err != nil {
		return result, fmt.Errorf("%w: could not encode request", pipeline.ErrRejected)
	}
	ctx, cancel := context.WithTimeout(ctx, i.options.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, i.options.URL, bytes.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("%w: could not create request", pipeline.ErrRejected)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", i.userAgent)
	if i.options.BearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+i.options.BearerToken)
	}
	response, err := i.client.Do(request)
	if err != nil {
		return result, i.transportError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	result.HTTPCode = response.StatusCode
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		detail := i.snippet(data)
		return result, i.failure(pipeline.KindRejected, pipeline.ErrRejected, response.StatusCode,
			fmt.Sprintf("HTTP %d: %s", response.StatusCode, detail), detail, nil)
	}
	// Preserve complete slot reports even when the remaining reply is malformed or truncated.
	parseErr := parseReport(data, &result)
	if readErr != nil || len(data) > maxBody {
		return result, i.failure(pipeline.KindResponseUnreadable, pipeline.ErrAmbiguous, response.StatusCode,
			"response could not be read within 1 MiB", i.snippet(data), nil)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return result, i.failure(pipeline.KindUnexpectedStatus, pipeline.ErrAmbiguous, response.StatusCode,
			fmt.Sprintf("unexpected HTTP %d (expected 200 or 202)", response.StatusCode), i.snippet(data), nil)
	}
	if parseErr != nil {
		return result, i.failure(pipeline.KindInvalidResponse, pipeline.ErrAmbiguous, response.StatusCode,
			"invalid per-slot response: "+i.redact(parseErr.Error()), i.snippet(data), nil)
	}
	return result, nil
}

// failure builds an InstallError whose text never contains the bearer token or the URL query.
func (i *Installer) failure(kind string, class error, code int, detail, body string, cause error) *pipeline.InstallError {
	return &pipeline.InstallError{Kind: kind, Class: class, HTTP: code, Detail: i.redact(detail), Body: body,
		URL: i.safeURL(), Cause: cause}
}

func (i *Installer) redact(text string) string {
	if i.options.BearerToken != "" {
		text = strings.ReplaceAll(text, i.options.BearerToken, "[redacted]")
	}
	return text
}

func (i *Installer) snippet(data []byte) string {
	text := i.redact(string(data))
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}

// safeURL drops credentials, query and fragment so the URL can be logged.
func (i *Installer) safeURL() string {
	u, err := url.Parse(i.options.URL)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// transportError names the real cause of a failed HTTP call; every one of these is ambiguous because the
// request may have reached the server before the connection broke.
func (i *Installer) transportError(ctx context.Context, err error) error {
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		err = urlErr.Err
	}
	var (
		unknownAuthority x509.UnknownAuthorityError
		hostname         x509.HostnameError
		invalidCert      x509.CertificateInvalidError
		recordHeader     tls.RecordHeaderError
		dns              *net.DNSError
		netErr           net.Error
	)
	kind, cause := pipeline.KindTransport, error(nil)
	switch {
	case ctx.Err() != nil:
		kind, cause = pipeline.KindTimeout, ctx.Err()
		if errors.Is(ctx.Err(), context.Canceled) {
			kind = pipeline.KindCanceled
		}
	case errors.As(err, &unknownAuthority):
		kind = pipeline.KindTLSUntrusted
	case errors.As(err, &hostname):
		kind = pipeline.KindTLSHostname
	case errors.As(err, &invalidCert):
		kind = pipeline.KindTLSInvalid
	case errors.As(err, &recordHeader):
		kind = pipeline.KindTLSNotTLS
	case strings.Contains(err.Error(), "tls:"):
		kind = pipeline.KindTLSOther
	case errors.As(err, &dns):
		kind = pipeline.KindDNS
	case errors.Is(err, syscall.ECONNREFUSED):
		kind = pipeline.KindConnectionRefused
	case errors.Is(err, syscall.ECONNRESET):
		kind = pipeline.KindConnectionReset
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), strings.Contains(err.Error(), "server closed idle connection"):
		kind = pipeline.KindConnectionClosed
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		kind = pipeline.KindUnreachable
	case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		kind = pipeline.KindTimeout
	}
	return i.failure(kind, pipeline.ErrAmbiguous, 0, i.redact(err.Error()), "", cause)
}

func parseReport(data []byte, result *pipeline.InstallResult) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil {
		return err
	}
	if opening != json.Delim('{') {
		return fmt.Errorf("expected object")
	}
	found := false
	seen := make(map[int]bool)
	var firstErr error
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		if key != "results" {
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return err
			}
			continue
		}
		if found {
			return fmt.Errorf("duplicate results")
		}
		found = true
		opening, err := decoder.Token()
		if err != nil {
			return err
		}
		if opening != json.Delim('[') {
			return fmt.Errorf("expected results array")
		}
		for decoder.More() {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return err
			}
			var item struct {
				Slot   int    `json:"slot"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(raw, &item); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if item.Slot < 1 || item.Slot > 100 || seen[item.Slot] {
				if firstErr == nil {
					firstErr = fmt.Errorf("invalid or duplicate slot")
				}
				continue
			}
			seen[item.Slot] = true
			switch item.Status {
			case "started":
				result.Started = append(result.Started, item.Slot)
			case "already_running":
				result.Busy = append(result.Busy, item.Slot)
			case "failed":
				result.Failed = append(result.Failed, item.Slot)
			default:
				if firstErr == nil {
					firstErr = fmt.Errorf("unknown status")
				}
			}
		}
		if _, err := decoder.Token(); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("missing results")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing response data")
	}
	return firstErr
}
