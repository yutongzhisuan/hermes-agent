package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ApprovalRequest describes one gated action awaiting a human decision.
type ApprovalRequest struct {
	JobID   string `json:"job_id"`
	Command string `json:"command"`
	Session string `json:"session"`
}

// ApprovalService asks an external authority whether to proceed.
// Returned errors are informational; the caller decides deny semantics
// (the bash tool treats any error as a denial).
type ApprovalService interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (bool, error)
}

// WebhookApproval POSTs the request and expects {"approved": bool}.
// Any transport error, non-2xx response, malformed body, or timeout is a denial (fail-closed).
type WebhookApproval struct {
	URL     string
	Timeout time.Duration // <= 0 defaults to 120s
	Client  *http.Client  // nil uses http.DefaultClient
}

func (w *WebhookApproval) EffectiveTimeout() time.Duration {
	if w.Timeout <= 0 {
		return 120 * time.Second
	}
	return w.Timeout
}

func (w *WebhookApproval) RequestApproval(ctx context.Context, req ApprovalRequest) (bool, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return false, fmt.Errorf("approval request encode: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, w.EffectiveTimeout())
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("approval request build: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := w.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return false, fmt.Errorf("approval webhook call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("approval webhook status %d", resp.StatusCode)
	}
	var decoded struct {
		Approved *bool `json:"approved"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil || decoded.Approved == nil {
		return false, fmt.Errorf("approval webhook malformed response: want {\"approved\": bool}")
	}
	return *decoded.Approved, nil
}
