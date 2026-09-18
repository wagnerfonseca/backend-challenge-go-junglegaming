package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

type claimRepository struct {
	db executor
}

func (r *claimRepository) Insert(ctx context.Context, claim financial.ReversalClaim) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO reversal_claims ("id", "referenceTransactionId", "reversalTransactionId", "createdAt")
		VALUES ($1, $2, $3, $4)`,
		claim.ID().String(),
		claim.ReferenceTransactionID().String(),
		claim.ReversalTransactionID().String(),
		claim.CreatedAt().UTC(),
	)
	return mapError(err)
}

func (r *claimRepository) ByReference(ctx context.Context, referenceTransactionID financial.TransactionID) (financial.ReversalClaim, bool, error) {
	row := r.db.QueryRow(ctx,
		`SELECT "id", "referenceTransactionId", "reversalTransactionId", "createdAt"
		FROM reversal_claims WHERE "referenceTransactionId" = $1`,
		referenceTransactionID.String(),
	)
	var (
		id                      string
		referenceID, reversalID string
		createdAt               time.Time
	)
	if err := row.Scan(&id, &referenceID, &reversalID, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return financial.ReversalClaim{}, false, nil
		}
		return financial.ReversalClaim{}, false, mapError(err)
	}
	claimID, err := financial.ParseReversalClaimID(id)
	if err != nil {
		return financial.ReversalClaim{}, false, corruptRow(err)
	}
	reference, err := financial.ParseTransactionID(referenceID)
	if err != nil {
		return financial.ReversalClaim{}, false, corruptRow(err)
	}
	reversal, err := financial.ParseTransactionID(reversalID)
	if err != nil {
		return financial.ReversalClaim{}, false, corruptRow(err)
	}
	claim, err := financial.RehydrateReversalClaim(claimID, reference, reversal, createdAt)
	if err != nil {
		return financial.ReversalClaim{}, false, corruptRow(err)
	}
	return claim, true, nil
}
