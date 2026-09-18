package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
)

type inboxRepository struct {
	db executor
}

// Begin inserts one delivery record or reports the stored digest when the
// (consumerName, messageId) pair already exists.
func (r *inboxRepository) Begin(ctx context.Context, delivery application.InboxDelivery) (bool, string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return false, "", fmt.Errorf("generating inbox identity: %w", err)
	}
	var inserted string
	err = r.db.QueryRow(ctx,
		`INSERT INTO inbox_deliveries ("id", "consumerName", "messageId", "digest", "receivedAt")
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT ("consumerName", "messageId") DO NOTHING
		RETURNING "id"`,
		id.String(), delivery.ConsumerName, delivery.MessageID, delivery.Digest, delivery.ReceivedAt.UTC(),
	).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		var digest string
		if err := r.db.QueryRow(ctx,
			`SELECT "digest" FROM inbox_deliveries WHERE "consumerName" = $1 AND "messageId" = $2`,
			delivery.ConsumerName, delivery.MessageID,
		).Scan(&digest); err != nil {
			return false, "", mapError(err)
		}
		return true, digest, nil
	}
	if err != nil {
		return false, "", mapError(err)
	}
	return false, "", nil
}

// Complete marks the delivery as durably handled in the current transaction.
func (r *inboxRepository) Complete(ctx context.Context, consumerName, messageID string, completedAt time.Time) error {
	_, err := r.db.Exec(ctx,
		`UPDATE inbox_deliveries SET "completedAt" = $3 WHERE "consumerName" = $1 AND "messageId" = $2`,
		consumerName, messageID, completedAt.UTC(),
	)
	return mapError(err)
}
