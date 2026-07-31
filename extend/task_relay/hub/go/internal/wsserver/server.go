package wsserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
)

const (
	jsonRPCParseError   = -32700
	jsonRPCInvalidReq   = -32600
	jsonRPCMethodNotFound = -32601
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// Server serves Mode A worker WebSocket JSON-RPC (Go Hub scaffold).
type Server struct {
	mux *http.ServeMux
}

// New constructs the WS JSON-RPC scaffold.
func New() *Server {
	s := &Server{mux: http.NewServeMux()}
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
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		resp, ok := dispatchJSONRPC(payload)
		if !ok {
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, resp); err != nil {
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

func dispatchJSONRPC(payload []byte) ([]byte, bool) {
	var req jsonRPCRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return marshalError(nil, jsonRPCParseError, fmt.Sprintf("parse error: %v", err)), true
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return marshalError(req.ID, jsonRPCInvalidReq, "not JSON-RPC 2.0"), true
	}
	switch req.Method {
	case "hub.ping":
		return marshalResult(req.ID, map[string]any{"ok": true}), true
	default:
		return marshalError(req.ID, jsonRPCMethodNotFound, "unknown method "+req.Method), true
	}
}

func marshalResult(id any, result map[string]any) []byte {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
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
