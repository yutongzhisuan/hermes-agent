package wsserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/auth"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

const (
	jsonRPCParseError     = -32700
	jsonRPCInvalidReq     = -32600
	jsonRPCMethodNotFound = -32601
	jsonRPCInvalidParams  = -32602
	jsonRPCDomainError    = -32000
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// Deps wires worker JSON-RPC handlers to Hub runtime services.
type Deps struct {
	Router *router.Router
	Auth   *auth.Auth
	Bus    *eventbus.Bus
}

// Server serves Mode A worker WebSocket JSON-RPC (Go Hub scaffold).
type Server struct {
	deps Deps
	mux  *http.ServeMux
}

// New constructs the WS JSON-RPC server.
func New(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.mux.HandleFunc("/ws", s.handleWS)
	return s
}

// Handler returns the HTTP handler for mounting or ListenAndServe.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// ListenAndServe starts the WS HTTP server until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.mux}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	claims, err := s.deps.Auth.VerifyWorkerJWT(token)
	if err != nil {
		http.Error(w, "invalid worker jwt", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	sess := &session{server: s, conn: conn, claims: claims}
	sess.serve()
}

type session struct {
	server    *Server
	conn      *websocket.Conn
	claims    *auth.WorkerClaims
	announced bool
}

func (s *session) serve() {
	for {
		_, payload, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		resp, ok := s.dispatch(payload)
		if !ok {
			continue
		}
		if err := s.conn.WriteMessage(websocket.TextMessage, resp); err != nil {
			return
		}
	}
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (s *session) dispatch(payload []byte) ([]byte, bool) {
	var req jsonRPCRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return marshalError(nil, jsonRPCParseError, fmt.Sprintf("parse error: %v", err)), true
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return marshalError(req.ID, jsonRPCInvalidReq, "not JSON-RPC 2.0"), true
	}
	switch req.Method {
	case "worker.announce":
		result, err := s.handleAnnounce(req.Params)
		if err != nil {
			return marshalError(req.ID, jsonRPCDomainError, err.Error()), true
		}
		return marshalResult(req.ID, result), true
	case "worker.poll":
		result, err := s.handlePoll(req.Params)
		if err != nil {
			return marshalError(req.ID, jsonRPCDomainError, err.Error()), true
		}
		return marshalResult(req.ID, result), true
	case "task.complete":
		result, err := s.handleComplete(req.Params)
		if err != nil {
			return marshalError(req.ID, jsonRPCDomainError, err.Error()), true
		}
		return marshalResult(req.ID, result), true
	case "hub.ping":
		return marshalResult(req.ID, map[string]any{"ok": true}), true
	default:
		return marshalError(req.ID, jsonRPCMethodNotFound, "unknown method "+req.Method), true
	}
}

func bearerToken(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 8 || !strings.EqualFold(value[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(value[7:])
}

func marshalResult(id any, result map[string]any) []byte {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return body
}

func marshalError(id any, code int, message string) []byte {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
	return body
}
