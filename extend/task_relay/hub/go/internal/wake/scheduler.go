package wake

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/registry"
)

const defaultWakeTTLSeconds = 60

// Scheduler issues HMAC wake tokens and POSTs Mode B wake URLs.
type Scheduler struct {
	registry     *registry.Registry
	secret       []byte
	relayWSURL   string
	wakeTTL      time.Duration
	httpClient   *http.Client
	consumed     map[string]struct{}
	consumedMu   sync.Mutex
	now          func() time.Time
}

// New constructs a wake scheduler.
func New(reg *registry.Registry, secret []byte, relayWSURL string, wakeTTLSeconds int) *Scheduler {
	if wakeTTLSeconds <= 0 {
		wakeTTLSeconds = defaultWakeTTLSeconds
	}
	return &Scheduler{
		registry:   reg,
		secret:     secret,
		relayWSURL: relayWSURL,
		wakeTTL:    time.Duration(wakeTTLSeconds) * time.Second,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		consumed:   make(map[string]struct{}),
		now:        time.Now,
	}
}

func (s *Scheduler) signWake(taskID, workerID string, expiresAt int64) string {
	payload := fmt.Sprintf("%s:%s:%d", taskID, workerID, expiresAt)
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// IssueWakeToken returns a single-use wake token and expiry timestamp.
func (s *Scheduler) IssueWakeToken(taskID, workerID string) (token string, expiresAt time.Time) {
	expiresAt = s.now().Add(s.wakeTTL)
	token = s.signWake(taskID, workerID, expiresAt.Unix())
	return token, expiresAt
}

// VerifyWakeToken validates and consumes a wake token for WS claim.
func (s *Scheduler) VerifyWakeToken(taskID, workerID, token string, expiresAt float64) bool {
	if s == nil || token == "" {
		return false
	}
	if s.now().Unix() > int64(expiresAt) {
		return false
	}
	s.consumedMu.Lock()
	defer s.consumedMu.Unlock()
	if _, ok := s.consumed[token]; ok {
		return false
	}
	expected := s.signWake(taskID, workerID, int64(expiresAt))
	if !hmac.Equal([]byte(expected), []byte(token)) {
		return false
	}
	s.consumed[token] = struct{}{}
	return true
}

// ScheduleWake POSTs a wake payload to the worker wake URL.
func (s *Scheduler) ScheduleWake(ctx context.Context, taskID, workerID string) bool {
	if s == nil || s.registry == nil {
		return false
	}
	worker := s.registry.Get(workerID)
	if worker == nil || worker.WakeURL == "" || !registry.SupportsMode(worker, "B") {
		return false
	}
	token, expiresAt := s.IssueWakeToken(taskID, workerID)
	body := map[string]any{
		"task_id":     taskID,
		"relay_url":   s.relayWSURL,
		"token":       token,
		"expires_at":  expiresAt.Unix(),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, worker.WakeURL, bytes.NewReader(raw))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted
}
