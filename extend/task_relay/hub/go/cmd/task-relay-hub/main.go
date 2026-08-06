package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/infa/task_relay/hub/internal/config"
	gohub "github.com/infa/task_relay/hub/internal/hub"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg, err := config.Parse(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runtime, err := gohub.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hub init: %v\n", err)
		return 1
	}
	defer runtime.Close()

	if err := runtime.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "hub: %v\n", err)
		return 1
	}
	return 0
}
