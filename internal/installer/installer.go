// Package installer sends additive install requests without automatic retries.
package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
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
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	version := "dev"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}
	return &Installer{options: o, client: &client, userAgent: "herzi/" + version}, nil
}

type requestBody struct {
	InfraEnv   string `json:"infra_env"`
	MCECluster string `json:"mce_cluster"`
	Namespace  string `json:"namespace"`
	Count      int    `json:"count"`
}

// Install never retries; ErrAmbiguous means machines may have started and requires a fresh sweep.
func (i *Installer) Install(ctx context.Context, req pipeline.InstallRequest) (pipeline.InstallResult, error) {
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
		// Transport errors may contain credentials; expose only classification and context sentinels.
		if ctx.Err() != nil {
			return result, fmt.Errorf("%w: %w", pipeline.ErrAmbiguous, ctx.Err())
		}
		return result, fmt.Errorf("%w: HTTP transport failed", pipeline.ErrAmbiguous)
	}
	defer func() { _ = response.Body.Close() }()
	result.HTTPCode = response.StatusCode
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		detail := string(data)
		if i.options.BearerToken != "" {
			detail = strings.ReplaceAll(detail, i.options.BearerToken, "[redacted]")
		}
		if len(detail) > 512 {
			detail = detail[:512]
		}
		return result, fmt.Errorf("%w: HTTP %d: %s", pipeline.ErrRejected, response.StatusCode, detail)
	}
	// Preserve complete slot reports even when the remaining reply is malformed or truncated.
	parseErr := parseReport(data, &result)
	if readErr != nil || len(data) > maxBody {
		return result, fmt.Errorf("%w: response could not be read within 1 MiB", pipeline.ErrAmbiguous)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return result, fmt.Errorf("%w: unexpected HTTP %d", pipeline.ErrAmbiguous, response.StatusCode)
	}
	if parseErr != nil {
		return result, fmt.Errorf("%w: invalid per-slot response", pipeline.ErrAmbiguous)
	}
	return result, nil
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
