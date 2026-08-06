package wsserver_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/auth"
	"github.com/infa/task_relay/hub/internal/delivery"
	"github.com/infa/task_relay/hub/internal/registry"
	"github.com/infa/task_relay/hub/internal/router"
	"github.com/infa/task_relay/hub/internal/store"
	"github.com/infa/task_relay/hub/internal/wsserver"
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
	require.NoError(t, json.Unmarshal(payload, &resp))
	result, ok := resp["result"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, result["ok"])
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
	require.NoError(t, err)

	conn := dialWS(t, ts.URL, token)
	defer conn.Close()

	writeRPC(t, conn, `{"jsonrpc":"2.0","id":1,"method":"worker.announce","params":{"worker_id":"worker-1","session_modes":["A"]}}`)
	readRPC(t, conn)

	writeRPC(t, conn, `{"jsonrpc":"2.0","id":2,"method":"worker.poll","params":{"max_tasks":1}}`)
	payload := readRPC(t, conn)
	var poll map[string]any
	require.NoError(t, json.Unmarshal(payload, &poll))
	result := poll["result"].(map[string]any)
	require.Equal(t, true, result["offered"])

	writeRPC(t, conn, `{"jsonrpc":"2.0","id":3,"method":"task.complete","params":{"task_id":"mode-a-1","status":"completed","summary":"done"}}`)
	payload = readRPC(t, conn)
	var complete map[string]any
	require.NoError(t, json.Unmarshal(payload, &complete))
	completeResult := complete["result"].(map[string]any)
	require.Equal(t, router.StatusCompleted, completeResult["status"])
}

func setupWS(t *testing.T) (*wsserver.Server, *router.Router, string) {
	t.Helper()
	verifier, err := auth.New("secret", "xhermes-relay-hub", "task-relay-hub", time.Hour, nil)
	require.NoError(t, err)
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
	require.NoError(t, err)
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
	require.NoError(t, err)
	return conn
}

func writeRPC(t *testing.T, conn *websocket.Conn, payload string) {
	t.Helper()
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(payload)))
}

func readRPC(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	_, payload, err := conn.ReadMessage()
	require.NoError(t, err)
	return payload
}
