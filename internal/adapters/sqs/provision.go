package sqs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Queue names provisioned by the local infrastructure.
const (
	IngressQueueName = "wager-transactions.fifo"
	DLQName          = "wager-transactions-dlq.fifo"
	EventQueueName   = "wager-events.fifo"
)

// Broker policy constants.
const (
	// MaxReceiveCount is the redrive threshold: the fifth receive moves the
	// message to the dead-letter queue.
	MaxReceiveCount = 5
	// VisibilityTimeout is the receive visibility applied while handling.
	VisibilityTimeout = 60
	// ReceiveWaitTime is the long-poll duration.
	ReceiveWaitTime = 20
	// DLQRetentionPeriod is the 14-day dead-letter retention.
	DLQRetentionPeriod = 1209600
)

// Queues carries the provisioned queue URLs.
type Queues struct {
	IngressURL string
	DLQURL     string
	EventURL   string
}

// ProvisionAPI is the subset of the SQS client used for provisioning.
type ProvisionAPI interface {
	GetQueueUrl(ctx context.Context, params *sqs.GetQueueUrlInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	CreateQueue(ctx context.Context, params *sqs.CreateQueueInput, optFns ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error)
	GetQueueAttributes(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

// Provision creates the ingress FIFO queue with its dead-letter queue (redrive
// after 5 receives) and the outbound event FIFO queue. Existing queues are
// reused.
func Provision(ctx context.Context, client ProvisionAPI) (Queues, error) {
	return ProvisionNamed(ctx, client, IngressQueueName, DLQName, EventQueueName)
}

// ProvisionNamed provisions one queue set under explicit names, so isolated
// environments can run side by side.
func ProvisionNamed(ctx context.Context, client ProvisionAPI, ingressName, dlqName, eventName string) (Queues, error) {
	dlqURL, dlqARN, err := ensureQueue(ctx, client, dlqName, map[string]string{
		string(types.QueueAttributeNameFifoQueue):              "true",
		string(types.QueueAttributeNameMessageRetentionPeriod): fmt.Sprintf("%d", DLQRetentionPeriod),
	})
	if err != nil {
		return Queues{}, fmt.Errorf("provisioning %s: %w", dlqName, err)
	}
	redrive, err := json.Marshal(map[string]string{
		"deadLetterTargetArn": dlqARN,
		"maxReceiveCount":     fmt.Sprintf("%d", MaxReceiveCount),
	})
	if err != nil {
		return Queues{}, err
	}
	ingressURL, _, err := ensureQueue(ctx, client, ingressName, map[string]string{
		string(types.QueueAttributeNameFifoQueue):                     "true",
		string(types.QueueAttributeNameContentBasedDeduplication):     "false",
		string(types.QueueAttributeNameVisibilityTimeout):             fmt.Sprintf("%d", VisibilityTimeout),
		string(types.QueueAttributeNameReceiveMessageWaitTimeSeconds): fmt.Sprintf("%d", ReceiveWaitTime),
		string(types.QueueAttributeNameRedrivePolicy):                 string(redrive),
	})
	if err != nil {
		return Queues{}, fmt.Errorf("provisioning %s: %w", ingressName, err)
	}
	eventURL, _, err := ensureQueue(ctx, client, eventName, map[string]string{
		string(types.QueueAttributeNameFifoQueue):                 "true",
		string(types.QueueAttributeNameContentBasedDeduplication): "false",
		string(types.QueueAttributeNameVisibilityTimeout):         fmt.Sprintf("%d", VisibilityTimeout),
	})
	if err != nil {
		return Queues{}, fmt.Errorf("provisioning %s: %w", eventName, err)
	}
	return Queues{IngressURL: ingressURL, DLQURL: dlqURL, EventURL: eventURL}, nil
}

func ensureQueue(ctx context.Context, client ProvisionAPI, name string, attributes map[string]string) (string, string, error) {
	output, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err == nil {
		url := aws.ToString(output.QueueUrl)
		arn, err := queueARN(ctx, client, url)
		return url, arn, err
	}
	created, createErr := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: attributes})
	if createErr != nil {
		return "", "", createErr
	}
	url := aws.ToString(created.QueueUrl)
	arn, err := queueARN(ctx, client, url)
	return url, arn, err
}

func queueARN(ctx context.Context, client ProvisionAPI, queueURL string) (string, error) {
	attributes, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return "", err
	}
	return attributes.Attributes[string(types.QueueAttributeNameQueueArn)], nil
}
