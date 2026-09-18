// Command provision creates the local SQS queues with their redrive policy
// before the service starts. It is the operational counterpart of the queue
// policy documents and is safe to re-run.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/config"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/sqs"
)

func main() {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := sqs.NewClient(ctx, cfg.SQS)
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("building the SQS client", "error", err.Error())
		os.Exit(1)
	}
	queues, err := sqs.Provision(ctx, client)
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("provisioning queues", "error", err.Error())
		os.Exit(1)
	}
	fmt.Printf("ingress=%s\ndlq=%s\nevents=%s\n", queues.IngressURL, queues.DLQURL, queues.EventURL)
}
