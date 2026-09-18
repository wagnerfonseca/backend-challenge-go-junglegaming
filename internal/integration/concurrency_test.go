//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/failpoint"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

// C90 - While one wallet is locked an operation on another wallet reaches its
// commit independently.
func TestCrossWalletConcurrency(t *testing.T) {
	h := newHarness(t)
	locked := h.openWallet("100.00")
	independent := h.openWallet("100.00")
	command := h.command(independent, financial.KindBet, "10.00")

	tx, err := adminPool.Begin(h.ctx())
	if err != nil {
		t.Fatalf("beginning lock transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(h.ctx()) }()
	if _, err := tx.Exec(h.ctx(), `SELECT 1 FROM wallets WHERE "id" = $1 FOR UPDATE`, locked.ID.String()); err != nil {
		t.Fatalf("locking wallet: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := h.service.SubmitWagerTransaction(context.Background(), command)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("independent wallet operation failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an operation on an independent wallet was blocked by another wallet's lock")
	}
	if got := h.walletBalance(independent.ID).MinorUnits(); got != 9000 {
		t.Errorf("independent balance = %d, want 9000", got)
	}
}

// C91 - One commit makes the transaction state, wallet balance/version,
// ledger, reversal claim and outbox events visible atomically.
func TestCommitAtomicity(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	bet := h.command(view, financial.KindBet, "25.00")
	result := h.mustSubmit(bet)

	var (
		state         string
		direction     string
		amount        int64
		balance       int64
		version       int64
		processed     int
		balanceEvents int
	)
	err := adminPool.QueryRow(h.ctx(), `
		SELECT t."state", l."direction", l."amount", w."balance", w."version",
			(SELECT count(*) FROM outbox_events o WHERE o."aggregateId" = t."walletId"::text AND o."eventType" = 'WagerTransactionProcessed'),
			(SELECT count(*) FROM outbox_events o WHERE o."aggregateId" = t."walletId"::text AND o."eventType" = 'WalletBalanceChanged')
		FROM wager_transactions t
		JOIN wallets w ON w."id" = t."walletId"
		JOIN wallet_ledger_entries l ON l."transactionId" = t."id"
		WHERE t."id" = $1`, result.TransactionID.String(),
	).Scan(&state, &direction, &amount, &balance, &version, &processed, &balanceEvents)
	if err != nil {
		t.Fatalf("reading the committed bet snapshot: %v", err)
	}
	if state != "PROCESSED" || direction != "DEBIT" || amount != 2500 || balance != 7500 || version != 2 {
		t.Errorf("committed bet = %s/%s/%d balance %d version %d", state, direction, amount, balance, version)
	}
	if processed != 2 || balanceEvents != 2 {
		t.Errorf("outbox events at commit = %d processed / %d balance, want 2/2", processed, balanceEvents)
	}

	refund := h.command(view, financial.KindRefund, "25.00")
	refund.ReferenceExternalID = bet.ExternalTransactionID
	refund.RoundID = bet.RoundID
	refundResult := h.mustSubmit(refund)
	var (
		refundState   string
		refundBalance int64
		claims        int
	)
	err = adminPool.QueryRow(h.ctx(), `
		SELECT t."state", w."balance", (SELECT count(*) FROM reversal_claims c WHERE c."reversalTransactionId" = t."id")
		FROM wager_transactions t
		JOIN wallets w ON w."id" = t."walletId"
		JOIN wallet_ledger_entries l ON l."transactionId" = t."id"
		WHERE t."id" = $1`, refundResult.TransactionID.String(),
	).Scan(&refundState, &refundBalance, &claims)
	if err != nil {
		t.Fatalf("reading the committed reversal snapshot: %v", err)
	}
	if refundState != "PROCESSED" || refundBalance != 10000 || claims != 1 {
		t.Errorf("reversal commit = %s balance %d claims %d, want PROCESSED/10000/1", refundState, refundBalance, claims)
	}
}

// C93 - Concurrent writers on one wallet preserve every committed balance
// update without a lost update.
func TestLostUpdatePrevention(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	const wins = 10
	commands := make([]application.SubmitWagerCommand, 0, wins)
	for i := 0; i < wins; i++ {
		commands = append(commands, h.command(view, financial.KindWin, "10.00"))
	}
	runParallel(t, h, commands)
	if got := h.walletBalance(view.ID).MinorUnits(); got != 100000+wins*1000 {
		t.Errorf("balance = %d, want %d", got, 100000+wins*1000)
	}
	if got := h.walletVersion(view.ID); got != 1+wins {
		t.Errorf("version = %d, want %d", got, 1+wins)
	}
	if got := h.ledgerForWallet(view.ID); got != 1+wins {
		t.Errorf("ledger entries = %d, want %d", got, 1+wins)
	}
}

// C94 - Two distinct 80.00 BRL bets against 100.00 BRL produce one PROCESSED,
// one REJECTED/INSUFFICIENT_FUNDS, final 20.00 BRL and one debit.
func TestConcurrentBetsTwoWallets(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	commands := []application.SubmitWagerCommand{
		h.command(view, financial.KindBet, "80.00"),
		h.command(view, financial.KindBet, "80.00"),
	}
	results := runParallel(t, h, commands)
	processed, rejected := 0, 0
	for _, result := range results {
		switch {
		case result.State == financial.StateProcessed:
			processed++
		case result.State == financial.StateRejected && result.FailureCode == financial.FailureInsufficientFunds:
			rejected++
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed/rejected = %d/%d, want 1/1 (%+v)", processed, rejected, results)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 2000 {
		t.Errorf("balance = %d, want 2000", got)
	}
	if got := h.ledgerForWallet(view.ID); got != 2 {
		t.Errorf("ledger entries = %d, want 2 (opening and one debit)", got)
	}
	if got := h.walletVersion(view.ID); got != 2 {
		t.Errorf("version = %d, want 2", got)
	}
}

// C95 - Replaying either bet from the dispute preserves both results, the
// 20.00 BRL balance and the single debit.
func TestReplayAfterDispute(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("100.00")
	commands := []application.SubmitWagerCommand{
		h.command(view, financial.KindBet, "80.00"),
		h.command(view, financial.KindBet, "80.00"),
	}
	first := runParallel(t, h, commands)
	replayed := runParallel(t, h, commands)
	for i := range first {
		if first[i].State != replayed[i].State || first[i].FailureCode != replayed[i].FailureCode ||
			first[i].TransactionID != replayed[i].TransactionID {
			t.Errorf("replay %d changed: %+v -> %+v", i, first[i], replayed[i])
		}
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 2000 {
		t.Errorf("balance = %d, want 2000", got)
	}
	if got := h.ledgerForWallet(view.ID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}
}

// C96 - The same valid bet submitted 50 times in parallel produces one debit
// and one transaction result.
func TestFiftyParallelBets(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	command := h.command(view, financial.KindBet, "25.00")
	commands := make([]application.SubmitWagerCommand, 50)
	for i := range commands {
		commands[i] = command
	}
	results := runParallel(t, h, commands)
	for _, result := range results {
		if result.State != financial.StateProcessed || result.TransactionID != results[0].TransactionID {
			t.Fatalf("result = %s/%s, want one PROCESSED transaction %s", result.State, result.TransactionID, results[0].TransactionID)
		}
	}
	if got := h.transactionsForWallet(view.ID); got != 2 {
		t.Errorf("transactions = %d, want 2 (opening and one bet)", got)
	}
	if got := h.ledgerForWallet(view.ID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 97500 {
		t.Errorf("balance = %d, want 97500", got)
	}
}

// C98 - A process stopping before its financial SQL transaction commits
// exposes no partial state and permits a safe retry.
func TestCommitPartialSafeRetry(t *testing.T) {
	var failFirst bool = true
	h := newHarnessWithOptions(t, application.WithTxFailpoint(func(stage string) error {
		if stage == "before_commit" && failFirst {
			failFirst = false
			return errors.New("simulated process stop before commit")
		}
		return nil
	}))
	view := h.openWallet("1000.00")
	command := h.command(view, financial.KindBet, "25.00")
	if _, err := h.submit(command); err == nil {
		t.Fatal("submission succeeded despite the pre-commit stop")
	}
	if h.transactionExists(command.ProviderID.String(), command.ExternalTransactionID) {
		t.Error("a pre-commit stop persisted the transaction")
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 100000 {
		t.Errorf("balance after pre-commit stop = %d, want 100000", got)
	}
	if got := h.ledgerForWallet(view.ID); got != 1 {
		t.Errorf("ledger entries after pre-commit stop = %d, want 1 (only the opening)", got)
	}
	if got := h.outboxCountOfType(view.ID, "WagerTransactionProcessed"); got != 1 {
		t.Errorf("processed events after pre-commit stop = %d, want 1 (only the opening)", got)
	}

	retried := h.mustSubmit(command)
	if retried.State != financial.StateProcessed {
		t.Errorf("retry state = %s, want PROCESSED", retried.State)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 97500 {
		t.Errorf("balance after retry = %d, want 97500", got)
	}
	if got := h.ledgerForWallet(view.ID); got != 2 {
		t.Errorf("ledger entries after retry = %d, want 2", got)
	}
}

// C99 - A process stopping after commit but before the SQS acknowledgement
// returns the committed result on redelivery without a second movement.
func TestSQSRedeliveryAfterCommit(t *testing.T) {
	h := newHarness(t)
	h.failpoints = failpoint.New([]string{failpoint.SQSAfterCommit})
	view := h.openWallet("1000.00")
	command := h.command(view, financial.KindBet, "25.00")
	envelope := envelopeFor(t, command, "message-"+newCorrelation())
	message := sqsMessage(envelope.MessageID, "sender-a", 1, envelopeJSON(t, envelope))

	broker := &sqsFake{}
	broker.enqueue(message)
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("post-commit failpoint did not stop the consumer")
			}
		}()
		_ = newConsumer(h, broker).PollOnce(context.Background())
	}()
	if !h.transactionExists(command.ProviderID.String(), command.ExternalTransactionID) {
		t.Fatal("the committed transaction is missing after the stopped acknowledgement")
	}
	if handles := broker.deletedHandles(); len(handles) != 0 {
		t.Fatalf("message was acknowledged before the crash: %v", handles)
	}
	ledger := h.ledgerForWallet(view.ID)

	h.failpoints = failpoint.New(nil)
	redelivery := &sqsFake{}
	redelivery.enqueue(message)
	if err := newConsumer(h, redelivery).PollOnce(context.Background()); err != nil {
		t.Fatalf("redelivery pass: %v", err)
	}
	if handles := redelivery.deletedHandles(); len(handles) != 1 {
		t.Errorf("redelivered message was not acknowledged: %v", handles)
	}
	if got := h.ledgerForWallet(view.ID); got != ledger {
		t.Errorf("ledger entries = %d, want %d (no second movement)", got, ledger)
	}
	if got := h.walletBalance(view.ID).MinorUnits(); got != 97500 {
		t.Errorf("balance = %d, want 97500", got)
	}
}

// C225 - Multiple reference workers poll due work: each item is owned by one
// 30-second lease in a claimed batch of at most 50.
func TestReferenceWorkerLease(t *testing.T) {
	h := newHarness(t)
	view := h.openWallet("1000.00")
	for i := 0; i < 55; i++ {
		refund := h.command(view, financial.KindRefund, "1.00")
		refund.ReferenceExternalID = newExternalID("bet")
		h.mustSubmit(refund)
	}
	now := time.Now().UTC()
	first, err := h.store.ClaimDueReferences(h.ctx(), now, 50, 30*time.Second)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	second, err := h.store.ClaimDueReferences(h.ctx(), now, 50, 30*time.Second)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(first) != 50 {
		t.Errorf("first batch = %d, want 50", len(first))
	}
	if len(second) != 5 {
		t.Errorf("second batch = %d, want 5", len(second))
	}
	seen := map[string]bool{}
	for _, transaction := range append(first, second...) {
		if seen[transaction.ID().String()] {
			t.Errorf("transaction %s was leased twice", transaction.ID())
		}
		seen[transaction.ID().String()] = true
	}
	var leased int
	if err := adminPool.QueryRow(h.ctx(),
		`SELECT count(*) FROM wager_transactions WHERE "state" = 'PENDING_REFERENCE' AND "claimedUntil" > $1`, now,
	).Scan(&leased); err != nil {
		t.Fatalf("counting leased work: %v", err)
	}
	if leased != 55 {
		t.Errorf("leased work = %d, want 55", leased)
	}
	var leaseSeconds float64
	if err := adminPool.QueryRow(h.ctx(),
		`SELECT extract(epoch FROM ("claimedUntil" - $1)) FROM wager_transactions WHERE "state" = 'PENDING_REFERENCE' LIMIT 1`, now,
	).Scan(&leaseSeconds); err != nil {
		t.Fatalf("reading lease duration: %v", err)
	}
	if leaseSeconds < 29 || leaseSeconds > 31 {
		t.Errorf("lease = %.1fs, want 30s", leaseSeconds)
	}
	recovered, err := h.store.ClaimDueReferences(h.ctx(), now.Add(31*time.Second), 50, 30*time.Second)
	if err != nil {
		t.Fatalf("recovery claim: %v", err)
	}
	if len(recovered) != 50 {
		t.Errorf("recovered batch = %d, want 50 after lease expiry", len(recovered))
	}
}

// runParallel submits every command concurrently and returns the results in
// command order.
func runParallel(t *testing.T, h *harness, commands []application.SubmitWagerCommand) []application.WagerResult {
	t.Helper()
	results := make([]application.WagerResult, len(commands))
	errs := make([]error, len(commands))
	var wait sync.WaitGroup
	for i, command := range commands {
		wait.Add(1)
		go func(index int, cmd application.SubmitWagerCommand) {
			defer wait.Done()
			results[index], errs[index] = h.service.SubmitWagerTransaction(context.Background(), cmd)
		}(i, command)
	}
	wait.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("parallel command %d failed: %v", i, err)
		}
	}
	return results
}
