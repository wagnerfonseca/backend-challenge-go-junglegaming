//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/sqs/consumer"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
)

// sqsFake is a deterministic in-memory SQS API used to prove consumer
// decisions without polling a real queue.
type sqsFake struct {
	mu         sync.Mutex
	batches    [][]types.Message
	received   []*awssqs.ReceiveMessageInput
	deleted    []string
	visibility []visibilityChange
	receiveErr error
}

type visibilityChange struct {
	Handle  string
	Timeout int32
}

func (f *sqsFake) ReceiveMessage(_ context.Context, params *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, params)
	if f.receiveErr != nil {
		return nil, f.receiveErr
	}
	if len(f.batches) == 0 {
		return &awssqs.ReceiveMessageOutput{}, nil
	}
	batch := f.batches[0]
	f.batches = f.batches[1:]
	return &awssqs.ReceiveMessageOutput{Messages: batch}, nil
}

func (f *sqsFake) DeleteMessage(_ context.Context, params *awssqs.DeleteMessageInput, _ ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, awssdk.ToString(params.ReceiptHandle))
	return &awssqs.DeleteMessageOutput{}, nil
}

func (f *sqsFake) ChangeMessageVisibility(_ context.Context, params *awssqs.ChangeMessageVisibilityInput, _ ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.visibility = append(f.visibility, visibilityChange{Handle: awssdk.ToString(params.ReceiptHandle), Timeout: params.VisibilityTimeout})
	return &awssqs.ChangeMessageVisibilityOutput{}, nil
}

func (f *sqsFake) enqueue(messages ...types.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, messages)
}

func (f *sqsFake) deletedHandles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

func (f *sqsFake) visibilityChanges() []visibilityChange {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]visibilityChange(nil), f.visibility...)
}

func (f *sqsFake) receiveCalls() []*awssqs.ReceiveMessageInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*awssqs.ReceiveMessageInput(nil), f.received...)
}

func sqsMessage(messageID, senderID string, receiveCount int, body string) types.Message {
	return types.Message{
		MessageId:     awssdk.String(messageID),
		ReceiptHandle: awssdk.String("receipt-" + messageID),
		Body:          awssdk.String(body),
		Attributes: map[string]string{
			"SenderId":                senderID,
			"ApproximateReceiveCount": strconv.Itoa(receiveCount),
		},
	}
}

func envelopeFor(t *testing.T, cmd application.SubmitWagerCommand, messageID string) consumer.Envelope {
	t.Helper()
	return consumer.Envelope{
		MessageID:  messageID,
		Type:       consumer.TypeWagerTransactionRequested,
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
		Data: consumer.Data{
			ProviderID:                     cmd.ProviderID.String(),
			ExternalTransactionID:          cmd.ExternalTransactionID.String(),
			IdempotencyKey:                 cmd.IdempotencyKey.String(),
			PlayerID:                       cmd.PlayerID.String(),
			WalletID:                       cmd.WalletID.String(),
			RoundID:                        cmd.RoundID.String(),
			GameID:                         cmd.GameID.String(),
			Kind:                           string(cmd.Kind),
			Money:                          consumer.MoneyPayload{Amount: cmd.Amount.String(), Currency: cmd.Amount.Currency()},
			ReferenceExternalTransactionID: cmd.ReferenceExternalID.String(),
		},
	}
}

func envelopeJSON(t *testing.T, envelope consumer.Envelope) string {
	t.Helper()
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshalling envelope: %v", err)
	}
	return string(encoded)
}

// newConsumer builds the real consumer over the fake broker API.
func newConsumer(h *harness, client *sqsFake) *consumer.Consumer {
	cfg := consumer.Config{
		QueueURL:     "http://fake/wager-transactions.fifo",
		ConsumerName: "wager-transactions",
		ProviderForSender: func(senderID string) (string, bool) {
			switch senderID {
			case "sender-a":
				return "provider-a", true
			case "sender-b":
				return "provider-b", true
			default:
				return "", false
			}
		},
	}
	return consumer.New(cfg, client, h.service,
		consumer.WithMetrics(h.runtimeMetrics()),
		consumer.WithFailpoints(h.failpoints),
	)
}

// sqsClient builds a real SQS client against LocalStack.
func sqsClient(t *testing.T) *awssqs.Client {
	t.Helper()
	if localstackEndpoint == "" {
		t.Fatal("localstack endpoint is not available")
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithBaseEndpoint(localstackEndpoint),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("loading aws config: %v", err)
	}
	return awssqs.NewFromConfig(cfg)
}
