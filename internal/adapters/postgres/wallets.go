package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

type walletRepository struct {
	db executor
}

const walletColumns = `"id", "playerId", "currency", "balance", "version", "createdAt", "updatedAt"`

func (r *walletRepository) Insert(ctx context.Context, wallet financial.Wallet) error {
	_, err := r.db.Exec(ctx, `INSERT INTO wallets (`+walletColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		wallet.ID().String(),
		wallet.PlayerID().String(),
		wallet.Currency(),
		wallet.Balance().MinorUnits(),
		wallet.Version(),
		wallet.CreatedAt().UTC(),
		wallet.UpdatedAt().UTC(),
	)
	return mapError(err)
}

func (r *walletRepository) LockByID(ctx context.Context, id financial.WalletID) (financial.Wallet, bool, error) {
	row := r.db.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets WHERE "id" = $1 FOR UPDATE`, id.String())
	return scanWallet(row)
}

func (r *walletRepository) ByID(ctx context.Context, id financial.WalletID) (financial.Wallet, bool, error) {
	row := r.db.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets WHERE "id" = $1`, id.String())
	return scanWallet(row)
}

func (r *walletRepository) SaveBalance(ctx context.Context, wallet financial.Wallet) error {
	_, err := r.db.Exec(ctx,
		`UPDATE wallets SET "balance" = $2, "version" = $3, "updatedAt" = $4 WHERE "id" = $1`,
		wallet.ID().String(),
		wallet.Balance().MinorUnits(),
		wallet.Version(),
		wallet.UpdatedAt().UTC(),
	)
	return mapError(err)
}

func scanWallet(row pgx.Row) (financial.Wallet, bool, error) {
	var (
		id                   string
		playerID             string
		currency             string
		balance              int64
		version              int64
		createdAt, updatedAt time.Time
	)
	err := row.Scan(&id, &playerID, &currency, &balance, &version, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return financial.Wallet{}, false, nil
		}
		return financial.Wallet{}, false, mapError(err)
	}
	return rehydrateWallet(id, playerID, currency, balance, version, createdAt, updatedAt)
}

func rehydrateWallet(id, playerID, currency string, balance, version int64, createdAt, updatedAt time.Time) (financial.Wallet, bool, error) {
	walletID, err := financial.ParseWalletID(id)
	if err != nil {
		return financial.Wallet{}, false, corruptRow(err)
	}
	pid, err := financial.ParsePlayerID(playerID)
	if err != nil {
		return financial.Wallet{}, false, corruptRow(err)
	}
	balanceMoney, err := money.FromMinorUnits(balance, currency)
	if err != nil {
		return financial.Wallet{}, false, corruptRow(err)
	}
	wallet, err := financial.RehydrateWallet(walletID, pid, currency, balanceMoney, version, createdAt, updatedAt)
	if err != nil {
		return financial.Wallet{}, false, corruptRow(err)
	}
	return wallet, true, nil
}
