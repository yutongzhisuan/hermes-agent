package hub_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/config"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/grpcserver"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	gohub "github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/hub"
	"github.com/infa/hermes-agent/extend/task_relay/master/go/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

func startTestHubGRPC(t *testing.T) (*gohub.Hub, func(context.Context, string) (net.Conn, error), func()) {
	t.Helper()
	cfg := config.Config{
		Host:        "127.0.0.1",
		GRPCPort:    0,
		DBPath:      filepath.Join(t.TempDir(), "relay.db"),
		JWTSecret:   "secret",
		JWTIssuer:   "hermes-relay-hub",
		JWTAudience: "task-relay-hub",
	}
	h, err := gohub.New(cfg)
	if err != nil {
		t.Fatalf("new hub: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(grpcserver.MasterAuthUnaryInterceptor(h.Auth())),
		grpc.StreamInterceptor(grpcserver.MasterAuthStreamInterceptor(h.Auth())),
	)
	pb.RegisterTaskRelayServer(srv, grpcserver.New(h.Router(), h.EventBus()))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}
	return h, dialer, func() {}
}

func TestGoHubGRPCDispatchTask(t *testing.T) {
	h, dialer, _ := startTestHubGRPC(t)
	token, err := h.Auth().IssueMasterJWT("master-1", time.Hour)
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	master, err := client.New(ctx, client.Config{
		Addr:      "passthrough:///bufnet",
		MasterJWT: token,
		ExtraDial: dialOpts,
	})
	if err != nil {
		t.Fatalf("master client: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })

	resp, err := master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId:        "go-hub-1",
		Goal:          "dispatch via go hub",
		CallbackTopic: "topic-go-hub",
	}, "sess", false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.IdempotentHit || resp.Status != pb.TaskStatus_TASK_STATUS_PENDING {
		t.Fatalf("unexpected dispatch response: %+v", resp)
	}

	again, err := master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId: "go-hub-1",
		Goal:   "dispatch via go hub",
	}, "sess", false)
	if err != nil || !again.IdempotentHit {
		t.Fatalf("idempotent dispatch: %+v err=%v", again, err)
	}
}

func TestGoHubGRPCGetTaskResult(t *testing.T) {
	h, dialer, _ := startTestHubGRPC(t)
	token, err := h.Auth().IssueMasterJWT("master-1", time.Hour)
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	master, err := client.New(ctx, client.Config{
		Addr:      "passthrough:///bufnet",
		MasterJWT: token,
		ExtraDial: dialOpts,
	})
	if err != nil {
		t.Fatalf("master client: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })

	_, err = master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId: "go-hub-result-1",
		Goal:   "get result",
	}, "sess", false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	pending, err := master.GetTaskResult(ctx, "go-hub-result-1", false)
	if err != nil {
		t.Fatalf("get pending result: %v", err)
	}
	if pending.Status != pb.TaskStatus_TASK_STATUS_PENDING || pending.TaskId != "go-hub-result-1" {
		t.Fatalf("unexpected pending result: %+v", pending)
	}

	_, err = h.Router().Complete(ctx, "go-hub-result-1", router.StatusLost, "worker timeout")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	completed, err := master.GetTaskResult(ctx, "go-hub-result-1", false)
	if err != nil {
		t.Fatalf("get completed result: %v", err)
	}
	if completed.Status != pb.TaskStatus_TASK_STATUS_LOST || completed.Summary != "worker timeout" {
		t.Fatalf("unexpected terminal result: %+v", completed)
	}
}

func TestGoHubGRPCRequiresMasterJWT(t *testing.T) {
	_, dialer, _ := startTestHubGRPC(t)
	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	master, err := client.New(ctx, client.Config{
		Addr:      "passthrough:///bufnet",
		ExtraDial: dialOpts,
	})
	if err != nil {
		t.Fatalf("master client: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })

	_, err = master.DispatchTask(ctx, &pb.TaskSpec{TaskId: "no-jwt"}, "sess", false)
	if err == nil {
		t.Fatal("expected unauthenticated dispatch without jwt")
	}
}

func TestGoHubGRPCCancelTask(t *testing.T) {
	h, dialer, _ := startTestHubGRPC(t)
	token, err := h.Auth().IssueMasterJWT("master-1", time.Hour)
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	master, err := client.New(ctx, client.Config{
		Addr:      "passthrough:///bufnet",
		MasterJWT: token,
		ExtraDial: dialOpts,
	})
	if err != nil {
		t.Fatalf("master client: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })

	_, err = master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId: "go-hub-cancel-1",
		Goal:   "cancel me",
	}, "sess", false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	resp, err := master.CancelTask(ctx, &pb.CancelTaskRequest{
		TaskId: "go-hub-cancel-1",
		Reason: "test cancel",
	})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(resp.CancelledTaskIds) != 1 || resp.CancelledTaskIds[0] != "go-hub-cancel-1" {
		t.Fatalf("unexpected cancel response: %+v", resp)
	}

	result, err := master.GetTaskResult(ctx, "go-hub-cancel-1", false)
	if err != nil {
		t.Fatalf("get result: %v", err)
	}
	if result.Status != pb.TaskStatus_TASK_STATUS_CANCELLED {
		t.Fatalf("unexpected status: %+v", result)
	}
}

func TestGoHubGRPCWatchTask(t *testing.T) {
	h, dialer, _ := startTestHubGRPC(t)
	token, err := h.Auth().IssueMasterJWT("master-1", time.Hour)
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	master, err := client.New(ctx, client.Config{
		Addr:      "passthrough:///bufnet",
		MasterJWT: token,
		ExtraDial: dialOpts,
	})
	if err != nil {
		t.Fatalf("master client: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })

	taskID := "go-hub-watch-1"
	_, err = master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId:        taskID,
		Goal:          "watch me",
		CallbackTopic: "watch-topic",
	}, "sess", false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	stream, err := master.Watch(ctx, client.WatchFilter{TaskID: taskID})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv status: %v", err)
	}
	if first.Kind != pb.TaskEventKind_TASK_EVENT_KIND_STATUS || first.TaskId != taskID {
		t.Fatalf("unexpected first event: %+v", first)
	}

	_, err = master.CancelTask(ctx, &pb.CancelTaskRequest{
		TaskId: taskID,
		Reason: "done watching",
	})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	second, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv terminal: %v", err)
	}
	if second.Kind != pb.TaskEventKind_TASK_EVENT_KIND_TERMINAL {
		t.Fatalf("unexpected terminal event: %+v", second)
	}
}

func TestGoHubGRPCListTasks(t *testing.T) {
	h, dialer, _ := startTestHubGRPC(t)
	token, err := h.Auth().IssueMasterJWT("master-1", time.Hour)
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	master, err := client.New(ctx, client.Config{
		Addr:      "passthrough:///bufnet",
		MasterJWT: token,
		ExtraDial: dialOpts,
	})
	if err != nil {
		t.Fatalf("master client: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })

	_, err = master.DispatchTask(ctx, &pb.TaskSpec{
		TaskId:        "list-task-1",
		Goal:          "listed",
		CallbackTopic: "list-topic",
	}, "sess", false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	list, err := master.ListTasks(ctx, &pb.ListTasksRequest{
		CallbackTopic: "list-topic",
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(list.Tasks) != 1 || list.Tasks[0].TaskId != "list-task-1" {
		t.Fatalf("unexpected list: %+v", list)
	}
}
