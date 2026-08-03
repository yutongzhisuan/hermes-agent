package tokenserver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/auth"
)

const tokenPath = "/v1/worker/token"

// Server serves worker JWT issuance and refresh over HTTP.
type Server struct {
	auth *auth.Auth
	mux  *http.ServeMux
}

// New constructs the worker token HTTP server.
func New(verifier *auth.Auth) *Server {
	s := &Server{auth: verifier, mux: http.NewServeMux()}
	s.mux.HandleFunc(tokenPath, s.handleIssueWorkerToken)
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

// ListenAndServe starts the token HTTP server until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string, tlsCfg *tls.Config) error {
	srv := &http.Server{Addr: addr, Handler: s.mux, TLSConfig: tlsCfg}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	if tlsCfg != nil {
		return srv.ListenAndServeTLS("", "")
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleIssueWorkerToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorJSON("invalid request body"))
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, errorJSON("invalid JSON body"))
		return
	}
	if workerJWT, ok := payload["worker_jwt"].(string); ok && workerJWT != "" {
		claims, err := s.auth.VerifyWorkerJWT(workerJWT)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errorJSON(err.Error()))
			return
		}
		token, err := s.auth.IssueWorkerJWT(claims.WorkerID, claims.AllowedToolsets, claims.MaxConcurrent, 0)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorJSON(err.Error()))
			return
		}
		expiresAt, err := auth.JWTExpiresAt(token)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorJSON(err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"worker_jwt": token, "expires_at": expiresAt})
		return
	}
	bootstrapToken, _ := payload["bootstrap_token"].(string)
	workerID, _ := payload["worker_id"].(string)
	if bootstrapToken == "" || workerID == "" {
		writeJSON(w, http.StatusBadRequest, errorJSON(
			"bootstrap_token and worker_id are required when worker_jwt is absent",
		))
		return
	}
	token, err := s.auth.ExchangeBootstrap(bootstrapToken, workerID)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorJSON(err.Error()))
		return
	}
	expiresAt, err := auth.JWTExpiresAt(token)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorJSON(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"worker_jwt": token, "expires_at": expiresAt})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func errorJSON(message string) map[string]any {
	return map[string]any{"error": message}
}

// ParseBootstrapTokens parses comma key=value bootstrap entries or inline JSON.
func ParseBootstrapTokens(raw string) (map[string]auth.BootstrapEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]auth.BootstrapEntry{}, nil
	}
	if strings.HasPrefix(raw, "{") {
		return parseBootstrapJSON(raw)
	}
	result := make(map[string]auth.BootstrapEntry)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		token, value, ok := strings.Cut(part, "=")
		if !ok || token == "" || value == "" {
			return nil, fmt.Errorf("bootstrap entry %q must be token=worker_id[:toolsets:max]", part)
		}
		fields := strings.Split(value, ":")
		entry := auth.BootstrapEntry{WorkerID: fields[0], MaxConcurrent: 1}
		if len(fields) > 1 && fields[1] != "" {
			entry.AllowedToolsets = strings.Split(fields[1], "+")
		}
		if len(fields) > 2 && fields[2] != "" {
			var maxConcurrent int
			if _, err := fmt.Sscanf(fields[2], "%d", &maxConcurrent); err != nil || maxConcurrent <= 0 {
				return nil, fmt.Errorf("invalid max_concurrent in bootstrap entry %q", part)
			}
			entry.MaxConcurrent = maxConcurrent
		}
		result[token] = entry
	}
	return result, nil
}

func parseBootstrapJSON(raw string) (map[string]auth.BootstrapEntry, error) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, err
	}
	result := make(map[string]auth.BootstrapEntry, len(data))
	for token, rawEntry := range data {
		var entry struct {
			WorkerID        string   `json:"worker_id"`
			AllowedToolsets []string `json:"allowed_toolsets"`
			MaxConcurrent   int      `json:"max_concurrent"`
		}
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			return nil, fmt.Errorf("bootstrap entry for %q: %w", token, err)
		}
		if entry.WorkerID == "" {
			return nil, fmt.Errorf("bootstrap entry for %q missing worker_id", token)
		}
		if entry.MaxConcurrent <= 0 {
			entry.MaxConcurrent = 1
		}
		result[token] = auth.BootstrapEntry{
			WorkerID:        entry.WorkerID,
			AllowedToolsets: append([]string(nil), entry.AllowedToolsets...),
			MaxConcurrent:   entry.MaxConcurrent,
		}
	}
	return result, nil
}
