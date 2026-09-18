package http

import (
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

type moneyInput struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m moneyInput) parse() (money.Money, error) { return money.Parse(m.Amount, m.Currency) }

type moneyView struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func newMoneyView(value money.Money) *moneyView {
	if !value.IsInitialized() {
		return nil
	}
	return &moneyView{Amount: value.String(), Currency: value.Currency()}
}

type openWalletRequest struct {
	PlayerID       string     `json:"playerId"`
	InitialBalance moneyInput `json:"initialBalance"`
}

type walletResponse struct {
	ID        string     `json:"id"`
	PlayerID  string     `json:"playerId"`
	Balance   *moneyView `json:"balance"`
	Version   int64      `json:"version"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

func newWalletResponse(view application.WalletView) walletResponse {
	return walletResponse{
		ID:        view.ID.String(),
		PlayerID:  view.PlayerID.String(),
		Balance:   newMoneyView(view.Balance),
		Version:   view.Version,
		CreatedAt: view.CreatedAt.UTC(),
		UpdatedAt: view.UpdatedAt.UTC(),
	}
}

type wagerRequest struct {
	ProviderID                     string     `json:"providerId"`
	ExternalTransactionID          string     `json:"externalTransactionId"`
	PlayerID                       string     `json:"playerId"`
	WalletID                       string     `json:"walletId"`
	RoundID                        string     `json:"roundId"`
	GameID                         string     `json:"gameId"`
	Kind                           string     `json:"kind"`
	Money                          moneyInput `json:"money"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId,omitempty"`
}

type transactionResponse struct {
	TransactionID                  string     `json:"transactionId"`
	Origin                         string     `json:"origin"`
	ProviderID                     string     `json:"providerId,omitempty"`
	ExternalTransactionID          string     `json:"externalTransactionId,omitempty"`
	WalletID                       string     `json:"walletId"`
	PlayerID                       string     `json:"playerId"`
	RoundID                        string     `json:"roundId,omitempty"`
	GameID                         string     `json:"gameId,omitempty"`
	Kind                           string     `json:"kind"`
	Money                          *moneyView `json:"money"`
	Status                         string     `json:"status"`
	Balance                        *moneyView `json:"balance,omitempty"`
	FailureCode                    string     `json:"failureCode,omitempty"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId,omitempty"`
	ReferenceDeadline              *time.Time `json:"referenceDeadline,omitempty"`
	CreatedAt                      *time.Time `json:"createdAt,omitempty"`
	UpdatedAt                      *time.Time `json:"updatedAt,omitempty"`
	IdempotentReplay               bool       `json:"idempotentReplay"`
}

func newTransactionResponse(result application.WagerResult) transactionResponse {
	response := transactionResponse{
		TransactionID:    result.TransactionID.String(),
		Origin:           string(result.Origin),
		ProviderID:       result.ProviderID.String(),
		WalletID:         result.WalletID.String(),
		PlayerID:         result.PlayerID.String(),
		RoundID:          result.RoundID.String(),
		GameID:           result.GameID.String(),
		Kind:             string(result.Kind),
		Money:            newMoneyView(result.Amount),
		Status:           string(result.State),
		Balance:          newMoneyView(result.ObservedBalance),
		IdempotentReplay: result.IdempotentReplay,
	}
	if !result.ExternalID.IsZero() {
		response.ExternalTransactionID = result.ExternalID.String()
	}
	if !result.ReferenceExternal.IsZero() {
		response.ReferenceExternalTransactionID = result.ReferenceExternal.String()
	}
	if result.FailureCode != "" {
		response.FailureCode = string(result.FailureCode)
	}
	if !result.ReferenceDeadline.IsZero() {
		deadline := result.ReferenceDeadline.UTC()
		response.ReferenceDeadline = &deadline
	}
	if !result.CreatedAt.IsZero() {
		created := result.CreatedAt.UTC()
		response.CreatedAt = &created
	}
	if !result.UpdatedAt.IsZero() {
		updated := result.UpdatedAt.UTC()
		response.UpdatedAt = &updated
	}
	return response
}

type ledgerEntryResponse struct {
	ID            string     `json:"id"`
	WalletID      string     `json:"walletId"`
	TransactionID string     `json:"transactionId"`
	Direction     string     `json:"direction"`
	Money         *moneyView `json:"money"`
	BalanceBefore *moneyView `json:"balanceBefore"`
	BalanceAfter  *moneyView `json:"balanceAfter"`
	CreatedAt     time.Time  `json:"createdAt"`
}

type ledgerPageResponse struct {
	Items      []ledgerEntryResponse `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

func newLedgerPageResponse(page application.LedgerPage) ledgerPageResponse {
	items := make([]ledgerEntryResponse, 0, len(page.Items))
	for _, entry := range page.Items {
		items = append(items, ledgerEntryResponse{
			ID:            entry.ID().String(),
			WalletID:      entry.WalletID().String(),
			TransactionID: entry.TransactionID().String(),
			Direction:     string(entry.Direction()),
			Money:         newMoneyView(entry.Amount()),
			BalanceBefore: newMoneyView(entry.BalanceBefore()),
			BalanceAfter:  newMoneyView(entry.BalanceAfter()),
			CreatedAt:     entry.CreatedAt().UTC(),
		})
	}
	return ledgerPageResponse{Items: items, NextCursor: page.NextCursor}
}

type reconciliationResponse struct {
	WalletID          string     `json:"walletId"`
	StoredBalance     *moneyView `json:"storedBalance"`
	CalculatedBalance *moneyView `json:"calculatedBalance"`
	Difference        *moneyView `json:"difference"`
	Consistent        bool       `json:"consistent"`
	CheckedEntries    int        `json:"checkedEntries"`
}

func newReconciliationResponse(report application.ReconciliationReport) reconciliationResponse {
	return reconciliationResponse{
		WalletID:          report.WalletID.String(),
		StoredBalance:     newMoneyView(report.StoredBalance),
		CalculatedBalance: newMoneyView(report.CalculatedBalance),
		Difference:        newMoneyView(report.Difference),
		Consistent:        report.Consistent,
		CheckedEntries:    report.CheckedEntries,
	}
}

type healthResponse struct {
	Status string `json:"status"`
}
