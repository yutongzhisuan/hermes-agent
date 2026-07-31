package wsserver_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/wsserver"
)

func TestHubPingJSONRPC(t *testing.T) {
	srv := wsserver.New()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, strings.Replace(ts.URL, "http", "ws", 1)+"/ws", nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(
		`{"jsonrpc":"2.0","id":1,"method":"hub.ping","params":{}}`,
	)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok || result["ok"] != true {
		t.Fatalf("unexpected response: %s", string(payload))
	}
}
