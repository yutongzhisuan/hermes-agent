package client

import (
	"context"
	"fmt"
	"time"

	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
)

// Config holds dial and observability settings for the Master-facing Hub gRPC client.
type Config struct {
	Addr      string
	MasterJWT string
	TLS       TLSConfig

	EnableMetrics bool
	EnableTracing bool
	OTelEndpoint  string

	ExtraDial []grpc.DialOption
}

// Client is a thin, framework-agnostic Task Relay Master SDK.
type Client struct {
	rpc     pb.TaskRelayClient
	conn    *grpc.ClientConn
	metrics *Metrics
}

// New dials the Hub and returns a client scoped with the Master JWT.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Addr == "" {
		return nil, fmt.Errorf("hub address is required")
	}

	creds, err := LoadTransportCredentials(cfg.TLS)
	if err != nil {
		return nil, err
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithUnaryInterceptor(bearerUnaryInterceptor(cfg.MasterJWT)),
		grpc.WithStreamInterceptor(bearerStreamInterceptor(cfg.MasterJWT)),
	}
	if cfg.EnableTracing {
		opts = append(opts, grpc.WithStatsHandler(otelgrpc.NewClientHandler()))
	}
	opts = append(opts, cfg.ExtraDial...)

	conn, err := grpc.NewClient(cfg.Addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial hub: %w", err)
	}

	var metrics *Metrics
	if cfg.EnableMetrics {
		metrics = NewMetrics(prometheusRegisterer())
	}

	return &Client{rpc: pb.NewTaskRelayClient(conn), conn: conn, metrics: metrics}, nil
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
	start := time.Now()
	AttachTraceToSpec(spec, ctx)
	resp, err := c.rpc.DispatchTask(ctx, &pb.DispatchTaskRequest{
		Spec:            spec,
		MasterSessionId: masterSessionID,
		AllowRedispatch: allowRedispatch,
	})
	c.metrics.observeRPC("DispatchTask", err, time.Since(start))
	if err == nil {
		c.metrics.incDispatch(resp.GetStatus().String(), false)
	}
	return resp, err
}

// DispatchTaskBatch sends a batch of TaskSpecs sharing one callback topic.
func (c *Client) DispatchTaskBatch(
	ctx context.Context,
	req *pb.DispatchTaskBatchRequest,
) (*pb.DispatchTaskBatchResponse, error) {
	start := time.Now()
	if req != nil {
		for _, spec := range req.GetSpecs() {
			AttachTraceToSpec(spec, ctx)
		}
	}
	resp, err := c.rpc.DispatchTaskBatch(ctx, req)
	c.metrics.observeRPC("DispatchTaskBatch", err, time.Since(start))
	if err == nil && resp != nil {
		for _, task := range resp.GetTasks() {
			c.metrics.incDispatch(task.GetStatus().String(), true)
		}
	}
	return resp, err
}

// GetTaskResult fetches the latest terminal or in-flight result for a task.
func (c *Client) GetTaskResult(
	ctx context.Context,
	taskID string,
	includeLatestCheckpoint bool,
) (*pb.TaskResult, error) {
	start := time.Now()
	resp, err := c.rpc.GetTaskResult(ctx, &pb.TaskResultRequest{
		TaskId:                  taskID,
		IncludeLatestCheckpoint: includeLatestCheckpoint,
	})
	c.metrics.observeRPC("GetTaskResult", err, time.Since(start))
	return resp, err
}

// CancelTask requests cancellation for one task or an entire batch.
func (c *Client) CancelTask(ctx context.Context, req *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error) {
	start := time.Now()
	resp, err := c.rpc.CancelTask(ctx, req)
	c.metrics.observeRPC("CancelTask", err, time.Since(start))
	return resp, err
}

// ListWorkers returns workers visible to the Master.
func (c *Client) ListWorkers(ctx context.Context, req *pb.ListWorkersRequest) (*pb.ListWorkersResponse, error) {
	start := time.Now()
	resp, err := c.rpc.ListWorkers(ctx, req)
	c.metrics.observeRPC("ListWorkers", err, time.Since(start))
	return resp, err
}

// ListTasks queries tasks with optional filters.
func (c *Client) ListTasks(ctx context.Context, req *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	start := time.Now()
	resp, err := c.rpc.ListTasks(ctx, req)
	c.metrics.observeRPC("ListTasks", err, time.Since(start))
	return resp, err
}

// Watch opens a server-streaming WatchTask subscription.
func (c *Client) Watch(ctx context.Context, filter WatchFilter) (pb.TaskRelay_WatchTaskClient, error) {
	start := time.Now()
	stream, err := c.watch(ctx, filter)
	c.metrics.observeRPC("WatchTask", err, time.Since(start))
	return stream, err
}

func (c *Client) watch(ctx context.Context, filter WatchFilter) (pb.TaskRelay_WatchTaskClient, error) {
	req := &pb.WatchTaskRequest{SinceEventId: filter.SinceEventID}
	switch {
	case filter.Topic != "":
		req.Filter = &pb.WatchTaskRequest_Topic{Topic: filter.Topic}
	case filter.BatchID != "":
		req.Filter = &pb.WatchTaskRequest_BatchId{BatchId: filter.BatchID}
	case filter.TaskID != "":
		req.Filter = &pb.WatchTaskRequest_TaskId{TaskId: filter.TaskID}
	default:
		return nil, fmt.Errorf("watch filter requires topic, batch_id, or task_id")
	}
	return c.rpc.WatchTask(ctx, req)
}
