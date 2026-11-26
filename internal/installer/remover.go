package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/team-redbull/herzi/internal/ids"
	"github.com/team-redbull/herzi/internal/pipeline"
)

// Remover sends remove-server-from-mce requests through the installer's client; it never retries.
type Remover struct{ i *Installer }

var _ pipeline.Remover = (*Remover)(nil)

// NewRemover takes the same options as New, with URL the complete remove route URL.
func NewRemover(o Options) (*Remover, error) {
	i, err := New(o)
	if err != nil {
		return nil, fmt.Errorf("remove client: %w", err)
	}
	return &Remover{i: i}, nil
}

type removeBody struct {
	ServerName string `json:"server_name"`
	MCECluster string `json:"mce_cluster"`
	Namespace  string `json:"namespace"`
}

// Remove wraps ErrAmbiguous when a run may have started; a 409 means one is already in flight and is not an error.
func (r *Remover) Remove(ctx context.Context, req pipeline.RemoveRequest) (pipeline.RemoveResult, error) {
	started := time.Now()
	result, err := r.remove(ctx, req)
	result.Duration = time.Since(started)
	return result, err
}

func (r *Remover) remove(ctx context.Context, req pipeline.RemoveRequest) (pipeline.RemoveResult, error) {
	var result pipeline.RemoveResult
	i := r.i
	if strings.TrimSpace(req.Server) == "" {
		return result, i.failure(pipeline.KindBadRequest, pipeline.ErrRejected, 0, "server name is empty", "", nil)
	}
	body, err := json.Marshal(removeBody{req.Server, i.options.MCECluster, i.options.Namespace})
	if err != nil {
		return result, i.failure(pipeline.KindBadRequest, pipeline.ErrRejected, 0, "could not encode request", "", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, i.options.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, i.options.URL, bytes.NewReader(body))
	if err != nil {
		return result, i.failure(pipeline.KindBadRequest, pipeline.ErrRejected, 0, "could not create request", "", nil)
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
	switch {
	case response.StatusCode == http.StatusConflict:
		result.AlreadyRunning = true
		return result, nil
	case response.StatusCode >= 400 && response.StatusCode < 500:
		detail := i.snippet(data)
		return result, i.failure(pipeline.KindRejected, pipeline.ErrRejected, response.StatusCode,
			fmt.Sprintf("HTTP %d: %s", response.StatusCode, detail), detail, nil)
	case readErr != nil || len(data) > maxBody:
		return result, i.failure(pipeline.KindResponseUnreadable, pipeline.ErrAmbiguous, response.StatusCode,
			"response could not be read within 1 MiB", i.snippet(data), nil)
	case response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted:
		return result, i.failure(pipeline.KindUnexpectedStatus, pipeline.ErrAmbiguous, response.StatusCode,
			fmt.Sprintf("unexpected HTTP %d (expected 200 or 202)", response.StatusCode), i.snippet(data), nil)
	}
	var started struct {
		WorkflowID string `json:"workflow_id"`
		RunID      string `json:"run_id"`
	}
	if err := json.Unmarshal(data, &started); err != nil || started.WorkflowID == "" {
		return result, i.failure(pipeline.KindInvalidResponse, pipeline.ErrAmbiguous, response.StatusCode,
			"reply is not {workflow_id, run_id}", i.snippet(data), nil)
	}
	result.WorkflowID, result.RunID = started.WorkflowID, started.RunID
	// herzi describes runs by this id; a different one means the id builder no longer matches the route.
	if want := ids.RemoveServer(req.Server); started.WorkflowID != want {
		return result, i.failure(pipeline.KindInvalidResponse, pipeline.ErrAmbiguous, response.StatusCode,
			fmt.Sprintf("started workflow %q, expected %q", started.WorkflowID, want), i.snippet(data), nil)
	}
	return result, nil
}
