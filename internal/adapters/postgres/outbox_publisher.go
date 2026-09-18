package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
)

// ClaimDueEvents leases a batch of due, unpublished outbox events with
// SKIP LOCKED so independent publisher instances never claim the same row.
func (s *Store) ClaimDueEvents(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]application.OutboxRecord, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE outbox_events
		SET "claimedUntil" = $2, "attempts" = "attempts" + 1
		WHERE "eventId" IN (
			SELECT "eventId" FROM outbox_events
			WHERE "publishedAt" IS NULL
				AND "nextAttemptAt" <= $1
				AND ("claimedUntil" IS NULL OR "claimedUntil" <= $1)
			ORDER BY "nextAttemptAt", "eventId"
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		RETURNING "eventId", "eventType", "aggregateId", "payload", "attempts"`,
		now.UTC(), now.Add(lease).UTC(), int64(limit),
	)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var records []application.OutboxRecord
	for rows.Next() {
		var record application.OutboxRecord
		if err := rows.Scan(&record.EventID, &record.EventType, &record.AggregateID, &record.Payload, &record.Attempts); err != nil {
			return nil, mapError(err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return records, nil
}

// MarkPublished confirms one publication. The event snapshot is never updated.
func (s *Store) MarkPublished(ctx context.Context, eventID string, publishedAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE outbox_events SET "publishedAt" = $2, "claimedUntil" = NULL WHERE "eventId" = $1`,
		eventID, publishedAt.UTC(),
	)
	return mapError(err)
}

// RescheduleEvent releases one lease and records the next attempt.
func (s *Store) RescheduleEvent(ctx context.Context, eventID string, now, nextAttemptAt time.Time, attempts int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE outbox_events SET "nextAttemptAt" = $2, "attempts" = $3, "claimedUntil" = NULL
		WHERE "eventId" = $1 AND "publishedAt" IS NULL`,
		eventID, nextAttemptAt.UTC(), int64(attempts),
	)
	return mapError(err)
}

// OldestPendingAge reports the age of the oldest unpublished event.
func (s *Store) OldestPendingAge(ctx context.Context, now time.Time) (time.Duration, bool, error) {
	var oldest *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT min("createdAt") FROM outbox_events WHERE "publishedAt" IS NULL`,
	).Scan(&oldest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, mapError(err)
	}
	if oldest == nil {
		return 0, false, nil
	}
	return now.Sub(*oldest), true, nil
}
