package client

import (
	"context"
	"fmt"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Config holds dial settings for the Master-facing Hub gRPC client.
type Config struct {
	Addr      string
	MasterJWT string
	TLS       bool
	ExtraDial []grpc.DialOption
}

// Client is a thin, framework-agnostic Task Relay Master SDK.
type Client struct {
	rpc  pb.TaskRelayClient
	conn *grpc.ClientConn
}

// New dials the Hub and returns a client scoped with the Master JWT.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Addr == "" {
		return nil, fmt.Errorf("hub address is required")
	}

	opts := []grpc.DialOption{
		grpc.WithUnaryInterceptor(bearerUnaryInterceptor(cfg.MasterJWT)),
		grpc.WithStreamInterceptor(bearerStreamInterceptor(cfg.MasterJWT)),
	}
	if !cfg.TLS {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	opts = append(opts, cfg.ExtraDial...)

	conn, err := grpc.NewClient(cfg.Addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial hub: %w", err)
	}

	return &Client{rpc: pb.NewTaskRelayClient(conn), conn: conn}, nil
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// DispatchTask sends a single TaskSpec to the Hub.
func (c *Client) DispatchTask(
	ctx context.Context,
	spec *pb.TaskSpec,
	masterSessionID string,
	allowRedispatch bool,
) (*pb.DispatchTaskResponse, error) {
	return c.rpc.DispatchTask(ctx, &pb.DispatchTaskRequest{
		Spec:              spec,
		MasterSessionId:   masterSessionID,
		AllowRedispatch:   allowRedispatch,
	})
}

// DispatchTaskBatch sends a batch of TaskSpecs sharing one callback topic.
func (c *Client) DispatchTaskBatch(
	ctx context.Context,
	req *pb.DispatchTaskBatchRequest,
) (*pb.DispatchTaskBatchResponse, error) {
	return c.rpc.DispatchTaskBatch(ctx, req)
}

// GetTaskResult fetches the latest terminal or in-flight result for a task.
func (c *Client) GetTaskResult(
	ctx context.Context,
	taskID string,
	includeLatestCheckpoint bool,
) (*pb.TaskResult, error) {
	return c.rpc.GetTaskResult(ctx, &pb.TaskResultRequest{
		TaskId:                  taskID,
		IncludeLatestCheckpoint: includeLatestCheckpoint,
	})
}

// CancelTask requests cancellation for one task or an entire batch.
func (c *Client) CancelTask(ctx context.Context, req *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error) {
	return c.rpc.CancelTask(ctx, req)
}

// ListWorkers returns workers visible to the Master.
func (c *Client) ListWorkers(ctx context.Context, req *pb.ListWorkersRequest) (*pb.ListWorkersResponse, error) {
	return c.rpc.ListWorkers(ctx, req)
}

// ListTasks queries tasks with optional filters.
func (c *Client) ListTasks(ctx context.Context, req *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	return c.rpc.ListTasks(ctx, req)
}
