package sqs

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
)

// SendAPI is the subset of the SQS client used by producers.
type SendAPI interface {
	SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

// EventSender publishes outbox snapshots to the event FIFO queue with a stable
// event identity: MessageGroupId=walletId and MessageDeduplicationId=eventId.
type EventSender struct {
	client   SendAPI
	queueURL string
}

// NewEventSender builds the outbound event publisher.
func NewEventSender(client SendAPI, queueURL string) *EventSender {
	return &EventSender{client: client, queueURL: queueURL}
}

// Publish sends one immutable outbox snapshot. The payload bytes are the
// stored snapshot bytes, so retries preserve the event identity and body.
func (s *EventSender) Publish(ctx context.Context, record application.OutboxRecord) error {
	_, err := s.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(s.queueURL),
		MessageBody:            aws.String(string(record.Payload)),
		MessageGroupId:         aws.String(record.AggregateID),
		MessageDeduplicationId: aws.String(record.EventID),
	})
	return err
}

// IngressSender publishes external wager requests to the ingress FIFO queue
// with MessageGroupId=walletId and MessageDeduplicationId=messageId. It models
// the provider-side producer in tests; production providers publish with their
// own IAM identity.
type IngressSender struct {
	client   SendAPI
	queueURL string
}

// NewIngressSender builds an ingress publisher.
func NewIngressSender(client SendAPI, queueURL string) *IngressSender {
	return &IngressSender{client: client, queueURL: queueURL}
}

// SendWagerRequest publishes one raw request envelope.
func (s *IngressSender) SendWagerRequest(ctx context.Context, messageID, walletID, body string) error {
	_, err := s.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(s.queueURL),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(walletID),
		MessageDeduplicationId: aws.String(messageID),
	})
	return err
}
