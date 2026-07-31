package metrics

import (
	"context"
	"fmt"
	"net/http"
)

const metricsPath = "/metrics"

// Handler serves Prometheus text metrics at /metrics.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(metricsPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(RenderPrometheus()))
	})
	return mux
}

// ListenAndServe starts the metrics HTTP server until ctx is cancelled.
func ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: Handler()}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("metrics server: %w", err)
	}
	return nil
}
