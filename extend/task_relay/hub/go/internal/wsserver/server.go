package wsserver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/auth"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/delivery"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/runpayload"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
)

const (
	jsonRPCParseError     = -32700
	jsonRPCInvalidReq     = -32600
	jsonRPCMethodNotFound = -32601
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// Deps wires worker JSON-RPC handlers to Hub runtime services.
type Deps struct {
	Router     *router.Router
	Auth       *auth.Auth
	Bus        *eventbus.Bus
	Registry   *registry.Registry
	Delivery   *delivery.Coordinator
	RunBuilder *runpayload.Builder
}

// Server serves worker WebSocket JSON-RPC (Go Hub port).
type Server struct {
	deps Deps
	mux  *http.ServeMux
}

// New constructs the WS JSON-RPC server.
func New(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.mux.HandleFunc("/ws", s.handleWS)
	s.mux.HandleFunc("/", s.handleWSRoot)
	return s
}

// Handler returns the HTTP handler for mounting or ListenAndServe.
func (s *Server) Handler() http.Handler { return s.mux }

// ListenAndServe starts the WS HTTP server until ctx is cancelled.
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

func (s *Server) handleWSRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.handleWS(w, r)
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
	defer func() {
		if sess.stopMonitor != nil {
			sess.stopMonitor()
		}
	}()
	sess.serve()
	if s.deps.Registry != nil {
		s.deps.Registry.UnregisterSession(claims.WorkerID)
	}
}

type session struct {
	server      *Server
	conn        *websocket.Conn
	claims      *auth.WorkerClaims
	pushMu      sync.Mutex
	monitorOnce sync.Once
	stopMonitor context.CancelFunc
	sessionID   string
	announced   bool
	modeC       bool
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
			return marshalError(req.ID, -32000, err.Error()), true
		}
		return marshalResult(req.ID, result, "worker.announce_ok"), true
	case "worker.poll":
		result, err := s.handlePoll(req.Params)
		if err != nil {
			return marshalError(req.ID, -32000, err.Error()), true
		}
		return marshalResult(req.ID, result, "worker.poll_ok"), true
	case "worker.claim":
		result, err := s.handleClaim(req.Params)
		if err != nil {
			return marshalError(req.ID, -32000, err.Error()), true
		}
		return marshalResult(req.ID, result, "worker.claim_ok"), true
	case "worker.nack":
		result, err := s.handleNack(req.Params)
		if err != nil {
			return marshalError(req.ID, -32000, err.Error()), true
		}
		return marshalResult(req.ID, result, "worker.nack_ok"), true
	case "cancel.ack":
		result, err := s.handleCancelAck(req.Params)
		if err != nil {
			return marshalError(req.ID, -32000, err.Error()), true
		}
		return marshalResult(req.ID, result, "cancel.ack_ok"), true
	case "task.progress":
		result, err := s.handleProgress(req.Params)
		if err != nil {
			return marshalError(req.ID, -32000, err.Error()), true
		}
		return marshalResult(req.ID, result, "task.progress_ok"), true
	case "task.checkpoint":
		result, err := s.handleCheckpoint(req.Params)
		if err != nil {
			return marshalError(req.ID, -32000, err.Error()), true
		}
		return marshalResult(req.ID, result, "checkpoint.ack"), true
	case "task.complete":
		result, err := s.handleComplete(req.Params)
		if err != nil {
			return marshalError(req.ID, -32000, err.Error()), true
		}
		return marshalResult(req.ID, result, "task.complete_ok"), true
	case "worker.heartbeat":
		result, err := s.handleHeartbeat(req.Params)
		if err != nil {
			return marshalError(req.ID, -32000, err.Error()), true
		}
		return marshalResult(req.ID, result, "worker.heartbeat_ok"), true
	case "worker.credit":
		result, err := s.handleCredit(req.Params)
		if err != nil {
			return marshalError(req.ID, -32000, err.Error()), true
		}
		return marshalResult(req.ID, result, "worker.credit_ok"), true
	case "worker.drain":
		result, err := s.handleDrain(req.Params)
		if err != nil {
			return marshalError(req.ID, -32000, err.Error()), true
		}
		return marshalResult(req.ID, result, "worker.drain_ok"), true
	case "hub.ping":
		return marshalResult(req.ID, map[string]any{"ok": true}, ""), true
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

func marshalResult(id any, result map[string]any, methodOK string) []byte {
	if methodOK != "" {
		result["method"] = methodOK
	}
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

func (s *Server) publishStatus(task *router.Task) {
	if s.deps.Bus == nil || task == nil {
		return
	}
	s.deps.Bus.Publish(eventbus.Event{
		TaskID:        task.TaskID,
		BatchID:       task.BatchID,
		CallbackTopic: task.CallbackTopic,
		Kind:          eventbus.KindStatus,
		Status:        task.Status,
		Summary:       task.Summary,
	})
}

func (s *Server) publishTerminal(task *router.Task) {
	if s.deps.Bus == nil || task == nil {
		return
	}
	s.deps.Bus.Publish(eventbus.Event{
		TaskID:        task.TaskID,
		BatchID:       task.BatchID,
		CallbackTopic: task.CallbackTopic,
		Kind:          eventbus.KindTerminal,
		Status:        task.Status,
		Summary:       task.Summary,
	})
}
