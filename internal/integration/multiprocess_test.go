//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	deadapter "github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/postgres"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/money"
)

// C142 - HTTP and SQS racing on one external operation persist one result
// regardless of which ingress wins.
func TestHTTPSQSRace(t *testing.T) {
	runtime := newHTTPRuntime(t)
	view := runtime.harness.openWallet("1000.00")
	command := runtime.harness.command(view, financial.KindBet, "25.00")
	broker := &sqsFake{}
	messageID := "race-" + newCorrelation()
	broker.enqueue(sqsMessage(messageID, "sender-a", 1, envelopeJSON(t, envelopeFor(t, command, messageID))))

	start := make(chan struct{})
	var (
		wait       sync.WaitGroup
		httpStatus int
		httpBody   []byte
		sqsErr     error
	)
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		httpStatus, httpBody = runtime.postWager(wagerJSONOf(command), command.IdempotencyKey.String())
	}()
	go func() {
		defer wait.Done()
		<-start
		sqsErr = newConsumer(runtime.harness, broker).PollOnce(context.Background())
	}()
	close(start)
	wait.Wait()

	requireStatus(t, httpStatus, 200, httpBody)
	if sqsErr != nil {
		t.Fatalf("SQS ingress failed during the race: %v", sqsErr)
	}
	if got := runtime.harness.transactionsForWallet(view.ID); got != 2 {
		t.Errorf("transactions = %d, want 2 (opening and one bet)", got)
	}
	if got := runtime.harness.ledgerForWallet(view.ID); got != 2 {
		t.Errorf("ledger entries = %d, want 2 (one debit)", got)
	}
	if got := runtime.harness.walletBalance(view.ID).MinorUnits(); got != 97500 {
		t.Errorf("balance = %d, want 97500", got)
	}
	if handles := broker.deletedHandles(); len(handles) != 1 {
		t.Errorf("SQS delivery acknowledgement = %v, want exactly one delete", handles)
	}
}

// C97 - Concurrency scenarios through three independent processes produce the
// same balances and ledger cardinality as one process.
func TestThreeProcessConsistency(t *testing.T) {
	h := newHarness(t)
	wallet := h.openWallet("100.00")
	commands := make([]application.SubmitWagerCommand, 20)
	for i := range commands {
		commands[i] = h.command(wallet, financial.KindBet, "10.00")
	}

	groups := [][]application.SubmitWagerCommand{commands[:7], commands[7:14], commands[14:]}
	results := make(chan []childResult, len(groups))
	errs := make(chan error, len(groups))
	var wait sync.WaitGroup
	for _, group := range groups {
		wait.Add(1)
		go func(batch []application.SubmitWagerCommand) {
			defer wait.Done()
			childResults, err := runChildProcess(batch)
			if err != nil {
				errs <- err
				return
			}
			results <- childResults
		}(group)
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("child process failed: %v", err)
	}

	processed, rejected, failed := 0, 0, 0
	seen := map[string]bool{}
	for batch := range results {
		for _, result := range batch {
			if result.Error != "" {
				failed++
				continue
			}
			if !seen[result.ExternalTransactionID] {
				seen[result.ExternalTransactionID] = true
			}
			switch {
			case result.Status == string(financial.StateProcessed):
				processed++
			case result.Status == string(financial.StateRejected) && result.FailureCode == string(financial.FailureInsufficientFunds):
				rejected++
			}
		}
	}
	if failed != 0 {
		t.Fatalf("child commands failed = %d, want 0", failed)
	}
	if processed != 10 || rejected != 10 {
		t.Fatalf("processed/rejected = %d/%d, want 10/10", processed, rejected)
	}
	if got := h.walletBalance(wallet.ID).MinorUnits(); got != 0 {
		t.Errorf("balance = %d, want 0", got)
	}
	if got := h.ledgerForWallet(wallet.ID); got != 11 {
		t.Errorf("ledger entries = %d, want 11 (opening and 10 debits)", got)
	}
	if got := h.transactionsForWallet(wallet.ID); got != 21 {
		t.Errorf("transactions = %d, want 21 (opening and 20 commands)", got)
	}

	// The same scenario in one process produces the same outcome.
	single := newHarness(t)
	singleWallet := single.openWallet("100.00")
	singleCommands := make([]application.SubmitWagerCommand, 20)
	for i := range singleCommands {
		singleCommands[i] = single.command(singleWallet, financial.KindBet, "10.00")
	}
	singleResults := runParallel(t, single, singleCommands)
	singleProcessed, singleRejected := 0, 0
	for _, result := range singleResults {
		switch result.State {
		case financial.StateProcessed:
			singleProcessed++
		case financial.StateRejected:
			singleRejected++
		}
	}
	if singleProcessed != processed || singleRejected != rejected {
		t.Errorf("single-process outcome = %d/%d, want the three-process %d/%d", singleProcessed, singleRejected, processed, rejected)
	}
	if got := single.walletBalance(singleWallet.ID).MinorUnits(); got != 0 {
		t.Errorf("single-process balance = %d, want 0", got)
	}
}

type childCommand struct {
	ProviderID            string `json:"providerId"`
	ExternalTransactionID string `json:"externalTransactionId"`
	IdempotencyKey        string `json:"idempotencyKey"`
	PlayerID              string `json:"playerId"`
	WalletID              string `json:"walletId"`
	RoundID               string `json:"roundId"`
	GameID                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Amount                string `json:"amount"`
	Currency              string `json:"currency"`
}

type childResult struct {
	ExternalTransactionID string `json:"externalTransactionId"`
	TransactionID         string `json:"transactionId"`
	Status                string `json:"status"`
	FailureCode           string `json:"failureCode"`
	Error                 string `json:"error"`
}

// runChildProcess executes one command batch in a real subprocess with its own
// memory and database connections.
func runChildProcess(commands []application.SubmitWagerCommand) ([]childResult, error) {
	payload := make([]childCommand, 0, len(commands))
	for _, command := range commands {
		payload = append(payload, childCommand{
			ProviderID:            command.ProviderID.String(),
			ExternalTransactionID: command.ExternalTransactionID.String(),
			IdempotencyKey:        command.IdempotencyKey.String(),
			PlayerID:              command.PlayerID.String(),
			WalletID:              command.WalletID.String(),
			RoundID:               command.RoundID.String(),
			GameID:                command.GameID.String(),
			Kind:                  string(command.Kind),
			Amount:                command.Amount.String(),
			Currency:              command.Amount.Currency(),
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		"WAGER_CHILD_COMMANDS="+string(encoded),
		"WAGER_CHILD_DSN="+childDSN(),
	)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("child exited: %v: %s", err, string(exitErr.Stderr))
		}
		return nil, err
	}
	var results []childResult
	if err := json.Unmarshal(output, &results); err != nil {
		return nil, fmt.Errorf("decoding child output %q: %w", string(output), err)
	}
	return results, nil
}

// runChildCommands is the subprocess entry point selected in TestMain.
func runChildCommands(payload string) int {
	var commands []childCommand
	if err := json.Unmarshal([]byte(payload), &commands); err != nil {
		fmt.Fprintf(os.Stderr, "decoding child commands: %v\n", err)
		return 1
	}
	pool, err := pgxpool.New(context.Background(), childDSN())
	if err != nil {
		fmt.Fprintf(os.Stderr, "opening child pool: %v\n", err)
		return 1
	}
	defer pool.Close()
	service := application.NewWagerService(deadapter.NewStore(pool), application.SystemClock{})
	results := make([]childResult, len(commands))
	var wait sync.WaitGroup
	for i, command := range commands {
		wait.Add(1)
		go func(index int, entry childCommand) {
			defer wait.Done()
			result, err := service.SubmitWagerTransaction(context.Background(), application.SubmitWagerCommand{
				ProviderID:            financial.ProviderID(entry.ProviderID),
				ExternalTransactionID: financial.ExternalID(entry.ExternalTransactionID),
				IdempotencyKey:        financial.IdempotencyKey(entry.IdempotencyKey),
				WalletID:              mustParseWallet(entry.WalletID),
				PlayerID:              mustParsePlayer(entry.PlayerID),
				RoundID:               financial.ExternalID(entry.RoundID),
				GameID:                financial.ExternalID(entry.GameID),
				Kind:                  financial.Kind(entry.Kind),
				Amount:                mustParseMoney(entry.Amount, entry.Currency),
			})
			results[index] = childResult{ExternalTransactionID: entry.ExternalTransactionID}
			if err != nil {
				results[index].Error = err.Error()
				return
			}
			results[index].TransactionID = result.TransactionID.String()
			results[index].Status = string(result.State)
			results[index].FailureCode = string(result.FailureCode)
		}(i, command)
	}
	wait.Wait()
	if err := json.NewEncoder(os.Stdout).Encode(results); err != nil {
		fmt.Fprintf(os.Stderr, "encoding child results: %v\n", err)
		return 1
	}
	return 0
}

func childDSN() string {
	if dsn := os.Getenv("WAGER_CHILD_DSN"); dsn != "" {
		return dsn
	}
	cfg, err := pgxpool.ParseConfig(testDSN)
	if err != nil {
		return testDSN
	}
	cfg.ConnConfig.User = "wager_app"
	cfg.ConnConfig.Password = "wager_app_local"
	return cfg.ConnConfig.ConnString()
}

func mustParseWallet(raw string) financial.WalletID {
	id, err := financial.ParseWalletID(raw)
	if err != nil {
		panic(err)
	}
	return id
}

func mustParsePlayer(raw string) financial.PlayerID {
	id, err := financial.ParsePlayerID(raw)
	if err != nil {
		panic(err)
	}
	return id
}

func mustParseMoney(amount, currency string) money.Money {
	value, err := money.Parse(amount, currency)
	if err != nil {
		panic(err)
	}
	return value
}
