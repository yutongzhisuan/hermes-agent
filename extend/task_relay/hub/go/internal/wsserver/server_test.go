package wsserver_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/auth"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/delivery"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/wsserver"
)

func TestHubPingJSONRPC(t *testing.T) {
	srv, _, token := setupWS(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn := dialWS(t, ts.URL, token)
	defer conn.Close()

	writeRPC(t, conn, `{"jsonrpc":"2.0","id":1,"method":"hub.ping","params":{}}`)
	payload := readRPC(t, conn)
	var resp map[string]any
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok || result["ok"] != true {
		t.Fatalf("unexpected response: %s", string(payload))
	}
}

func TestModeAPollComplete(t *testing.T) {
	srv, rt, token := setupWS(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx := context.Background()
	_, err := rt.DispatchTask(ctx, router.TaskSpec{
		TaskID: "mode-a-1",
		Goal:   "execute via poll",
	}, "test-session", false)
	if err != nil {
		t.Fatal(err)
	}

	conn := dialWS(t, ts.URL, token)
	defer conn.Close()

	writeRPC(t, conn, `{"jsonrpc":"2.0","id":1,"method":"worker.announce","params":{"worker_id":"worker-1","session_modes":["A"]}}`)
	readRPC(t, conn)

	writeRPC(t, conn, `{"jsonrpc":"2.0","id":2,"method":"worker.poll","params":{"max_tasks":1}}`)
	payload := readRPC(t, conn)
	var poll map[string]any
	if err := json.Unmarshal(payload, &poll); err != nil {
		t.Fatal(err)
	}
	result := poll["result"].(map[string]any)
	if result["offered"] != true {
		t.Fatalf("expected offered task: %s", string(payload))
	}

	writeRPC(t, conn, `{"jsonrpc":"2.0","id":3,"method":"task.complete","params":{"task_id":"mode-a-1","status":"completed","summary":"done"}}`)
	payload = readRPC(t, conn)
	var complete map[string]any
	if err := json.Unmarshal(payload, &complete); err != nil {
		t.Fatal(err)
	}
	completeResult := complete["result"].(map[string]any)
	if completeResult["status"] != router.StatusCompleted {
		t.Fatalf("unexpected complete: %s", string(payload))
	}
}

func setupWS(t *testing.T) (*wsserver.Server, *router.Router, string) {
	t.Helper()
	verifier, err := auth.New("secret", "hermes-relay-hub", "task-relay-hub", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := router.NewRouter(store.NewMemory(), nil, router.DefaultRouterConfig())
	reg := registry.New(nil)
	buildRun := func(ctx context.Context, claimed router.ClaimedTask) (map[string]any, error) {
		return map[string]any{"run": map[string]any{"task_id": claimed.TaskID, "goal": claimed.Goal}}, nil
	}
	srv := wsserver.New(wsserver.Deps{
		Router: rt, Auth: verifier, Registry: reg,
		Delivery: delivery.New(rt, reg, nil, buildRun),
	})
	token, err := verifier.IssueWorkerJWT("worker-1", []string{"terminal"}, 2, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return srv, rt, token
}

func dialWS(t *testing.T, baseURL, token string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	header := make(map[string][]string)
	header["Authorization"] = []string{"Bearer " + token}
	conn, _, err := websocket.DefaultDialer.DialContext(
		ctx,
		strings.Replace(baseURL, "http", "ws", 1)+"/ws",
		header,
	)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	return conn
}

func writeRPC(t *testing.T, conn *websocket.Conn, payload string) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readRPC(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return payload
}
