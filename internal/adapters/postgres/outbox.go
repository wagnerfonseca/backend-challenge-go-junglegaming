package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/event"
)

type outboxRepository struct {
	db executor
}

func (r *outboxRepository) Insert(ctx context.Context, envelope event.Envelope, now time.Time) error {
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshalling event %s: %w", envelope.EventID, err)
	}
	_, err = r.db.Exec(ctx,
		`INSERT INTO outbox_events ("eventId", "eventType", "aggregateId", "correlationId", "causationId", "occurredAt", "version", "payload", "createdAt", "nextAttemptAt")
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`,
		envelope.EventID,
		string(envelope.EventType),
		envelope.AggregateID,
		envelope.CorrelationID,
		nullString(envelope.CausationID),
		envelope.OccurredAt.Time().UTC(),
		envelope.Version,
		payload,
		now.UTC(),
	)
	return mapError(err)
}
