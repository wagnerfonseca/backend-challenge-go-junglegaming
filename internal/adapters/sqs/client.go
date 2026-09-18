// Package sqs is the broker adapter: queue provisioning, FIFO publishing and
// the durable-inbox consumer.
package sqs

import (
	"context"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/config"
)

// NewClient builds the SQS client from the broker configuration.
func NewClient(ctx context.Context, cfg config.SQS) (*sqs.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region), awsconfig.WithBaseEndpoint(cfg.Endpoint))
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(awsCfg), nil
}

// Ping verifies one queue is reachable through GetQueueAttributes.
func Ping(ctx context.Context, client *sqs.Client, queueURL string) error {
	_, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       &queueURL,
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	return err
}
