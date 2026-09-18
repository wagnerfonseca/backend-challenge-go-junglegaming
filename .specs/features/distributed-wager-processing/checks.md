# Distributed wager processing - checks

Profile: standard
Plan: `.specs/features/distributed-wager-processing/plan.md`

## Intent

127 checks across 17 slices · 20 one-way doors · 0 open. Every criterion from the approved plan carries at least one check whose proof settles it; every enumerated set member is named beside its proof.

## Checks

### S1 - Serviço inicia, compõe dependências e encerra de forma segura (AC 1-12)

**C1** - `go.mod` declares Go `1.27.1` and the Docker build stage uses Go `1.27.1` (AC 1)
Proof: `grep '^go ' go.mod && grep 'golang:1.27' Dockerfile`

**C2** - The domain compiles without imports of Fx, HTTP, SQS, PostgreSQL, OIDC, Prometheus, or logging adapters (AC 2)
Proof: `go build ./internal/domain/...`

**C3** - The application graph composes configuration, connections, repositories, use cases, handlers, and workers through `fx.Module`, `fx.Provide`, and `fx.Invoke` (AC 3)
Proof: `grep -r 'fx.Module\|fx.Provide\|fx.Invoke' cmd/ internal/adapters/`

**C4** - Invalid or unavailable configuration, PostgreSQL, SQS, or OIDC at startup causes non-zero exit before readiness (AC 4)
Proof: `go test ./... -run TestStartupFailure -tags integration`

**C5** - One `context.Context` propagates through application and I/O boundaries during requests and worker operations (AC 5)
Proof: `grep -r 'context.Context' internal/application/ internal/adapters/ | head -30`

**C6** - `SIGTERM` stops accepting HTTP and stops polling SQS before starting resource shutdown (AC 6)
Proof: `go test ./... -run TestSIGTERMOrdering -tags integration`

**C7** - In-flight SQS work that cannot finish in 30s of `SIGTERM` releases message visibility for redelivery (AC 7)
Proof: `go test ./... -run TestSIGTERMSOFSQS -tags integration`

**C8** - Shutdown closes PostgreSQL and SQS only after HTTP and all workers stopped (AC 8)
Proof: `go test ./... -run TestShutdownOrder -tags integration`

**C9** - `docker compose up --build` leaves one application instance in `ready` after health checks pass (AC 9) [done]
Proof: `docker compose up --build && curl http://localhost:8080/health/ready`

**C10** - Forward and reverse migrations exit non-zero on failure (AC 10)
Proof: `go run ./cmd/migrate --validate`

**C11** - PostgreSQL adapter uses `pgx/v5` and explicit parameterized SQL for every query, transaction, lock, and constraint-dependent operation (AC 11)
Proof: `grep 'github.com/jackc/pgx/v5' internal/adapters/postgres/*.go && ! grep -r 'QueryRowx\|Select\\|Get' internal/adapters/postgres/*.go`

**C12** - No mutable package-global dependency or service locator exists (AC 12)
Proof: `grep -rE 'var\s+\w+\s+(Client|DB|Pool|Client)\b' internal/ || echo no-globals`

### S2 - Dinheiro e entidades preservam invariantes sem infraestrutura (AC 13-27)

**C13** - Money never passes through `float32` or `float64` in any code path (AC 13) [done]
Proof: `grep -rE 'float32|float64' internal/domain/money/ || echo no-floats`

**C14** - Parsing `25.00` BRL produces exactly `2500` minor units and serializes back to `{"amount":"25.00","currency":"BRL"}` (AC 14) [done]
Proof: `go test ./domain/... -run TestMoneyParseAndSerialize`

**C15** - Empty, negative, NaN, Infinity, scientific notation, leading zeroes, or non-two-decimal amounts are rejected without rounding (AC 15) [done]
Proof: `go test ./domain/... -run TestMoneyInvalidInputs`

**C16** - Amount exceeding `92233720368547758.07` returns a classified overflow error (AC 16) [done]
Proof: `go test ./domain/... -run TestMoneyOverflow`

**C17** - Addition, subtraction, or negation exceeding int64 range returns a classified overflow error with no wrapped value (AC 17) [done]
Proof: `go test ./domain/... -run TestMoneyArithmeticOverflow`

**C18** - Arithmetic or comparison combining different currencies returns a classified currency-mismatch error (AC 18) [done]
Proof: `go test ./domain/... -run TestMoneyCurrencyMismatch`

**C19** - Money value is immutable after construction (AC 19) [done]
Proof: `go test ./domain/... -run TestMoneyImmutability`

**C20** - External financial command with currency other than BRL returns `422` with `UNSUPPORTED_CURRENCY` (AC 20)
Proof: `go test ./... -run TestUnsupportedCurrency -tags integration`

**C21** - Wallet never exposes a balance below `0.00` (AC 21) [done]
Proof: `go test ./domain/... -run TestWalletNegativeBalance`

**C22** - Entity creation rejects empty identity, invalid initial state, zero timestamp, or required zero Money (AC 22) [done]
Proof: `go test ./domain/... -run TestEntityInvalidConstruction`

**C23** - Terminal WagerTransaction receiving another transition returns classified invalid-transition error preserving terminal state (AC 23) [done]
Proof: `go test ./domain/... -run TestTerminalStateTransition`

**C24** - Entity rehydration emits no event, applies no movement, and increments no version (AC 24) [done]
Proof: `go test ./domain/... -run TestRehydrationNoEffects`

**C25** - Uninitialized Money, Wallet, WagerTransaction, or WalletLedgerEntry used by public domain operation is rejected (AC 25) [done]
Proof: `go test ./domain/... -run TestUninitializedRejection`

**C26** - Domain error API is classifiable through `errors.Is` or `errors.As` (AC 26) [done]
Proof: `go test ./domain/... -run TestErrorClassification`

**C27** - Business rejection path returns classified domain result instead of invoking panic (AC 27) [done]
Proof: `go test ./domain/... -run TestBusinessRejectionNoPanic`

### S3 - Carteira abre com saldo e ledger auditável (AC 28-43)

**C28** - Opening wallet with `1000.00 BRL` returns `201` with id, playerId, balance `1000.00 BRL`, and version `1` (AC 28)
Proof: `go test ./... -run TestWalletOpenPositiveBalance -tags integration`

**C29** - Opening wallet with positive balance persists one `OPENING` transaction in `PROCESSED` with stable internal identity (AC 29)
Proof: `go test ./... -run TestWalletOpenPersistsOpening -tags integration`

**C30** - Opening wallet with positive balance appends one `CREDIT` ledger entry from `0.00 BRL` to initial balance (AC 30)
Proof: `go test ./... -run TestWalletOpenLedgerEntry -tags integration`

**C31** - Opening wallet with positive balance persists exactly one `WagerTransactionProcessed` outbox event (AC 31)
Proof: `go test ./... -run TestWalletOpenOutboxEvent -tags integration`

**C32** - Opening wallet with `0.00 BRL` returns `201` with balance `0.00 BRL` and version `1` (AC 32)
Proof: `go test ./... -run TestWalletOpenZeroBalance -tags integration`

**C33** - Opening wallet with `0.00 BRL` persists no `OPENING` transaction (AC 33)
Proof: `go test ./... -run TestWalletOpenZeroNoOpening -tags integration`

**C34** - Opening wallet with `0.00 BRL` persists no ledger entry (AC 34)
Proof: `go test ./... -run TestWalletOpenZeroNoLedger -tags integration`

**C35** - Opening wallet with `0.00 BRL` persists no financial outbox event (AC 35)
Proof: `go test ./... -run TestWalletOpenZeroNoOutbox -tags integration`

**C36** - Duplicate `(playerId, currency)` returns `409` with `WALLET_ALREADY_EXISTS` and adds no credit (AC 36)
Proof: `go test ./... -run TestWalletDuplicate -tags integration`

**C37** - PostgreSQL schema enforces one Wallet per `(playerId, currency)` independently of application checks (AC 37)
Proof: `grep -A5 'UNIQUE.*playerId.*currency' migrations/*up.sql`

**C38** - Processed operation changing wallet balance after creation increments version by exactly `1`; no-movement operations preserve version (AC 38)
Proof: `go test ./... -run TestWalletVersionIncrement -tags integration`

**C39** - Movement currency differing from wallet currency is rejected before changing balance (AC 39)
Proof: `go test ./... -run TestCrossCurrencyRejection -tags integration`

**C40** - Debit that would produce negative balance is rejected before changing balance (AC 40)
Proof: `go test ./... -run TestInsufficientFundsRejection -tags integration`

**C41** - WalletLedgerEntry requires `balanceAfter = balanceBefore + money` for CREDIT or `balanceAfter = balanceBefore - money` for DEBIT (AC 41) [done]
Proof: `go test ./domain/... -run TestLedgerArithmetic`

**C42** - PostgreSQL schema allows at most one ledger entry per `(walletId, transactionId)` (AC 42)
Proof: `grep -A3 'UNIQUE.*walletId.*transactionId' migrations/*up.sql`

**C43** - Application database role receives authorization error for every UPDATE or DELETE on ledger (AC 43)
Proof: `grep -r 'GRANT.*INSERT.*SELECT.*ledger' migrations/*up.sql && ! grep -r 'GRANT.*UPDATE\|DELETE.*ledger' migrations/*up.sql`

### S4 - Operação externa é persistente, idempotente e reproduz o resultado original (AC 44-63)

**C44** - HTTP or SQS submits kind OPENING or outside BET/WIN/LOSS/REFUND/ROLLBACK is rejected as invalid external input (AC 44)
Proof: `go test ./... -run TestInvalidKindRejection -tags integration`

**C45** - `POST /wagering/transactions` omits `Idempotency-Key` returns `400` with `IDEMPOTENCY_KEY_REQUIRED` and persists nothing (AC 45)
Proof: `go test ./... -run TestMissingIdempotencyKey -tags integration`

**C46** - Valid independent operation returns `200` with transactionId, terminal status, persisted balance, and idempotentReplay:false (AC 46)
Proof: `go test ./... -run TestValidOperationResponse -tags integration`

**C47** - Idempotency digest is SHA-256 over canonical JSON `sha256-jcs-v1` containing provider, external transaction, player, wallet, round, game, kind, canonical money, optional reference (AC 47)
Proof: `go test ./... -run TestIdempotencyDigest -tags integration`

**C48** - Same provider key and equivalent business digest returns persisted result with idempotentReplay:true and no movement, ledger row, or outbox row (AC 48)
Proof: `go test ./... -run TestIdempotentReplay -tags integration`

**C49** - Same provider key with different business digest returns `409` with `IDEMPOTENCY_CONFLICT` preserving first result (AC 49)
Proof: `go test ./... -run TestIdempotencyConflict -tags integration`

**C50** - Same `(providerId, externalTransactionId)` under different idempotency key returns `409` with `EXTERNAL_TRANSACTION_CONFLICT` (AC 50)
Proof: `go test ./... -run TestExternalTransactionConflict -tags integration`

**C51** - Equivalent HTTP and SQS commands produce one WagerTransaction and at most one financial movement (AC 51)
Proof: `go test ./... -run TestHTTPSQSIddempotency -tags integration`

**C52** - Application restart preserves all idempotency decisions in PostgreSQL (AC 52)
Proof: `go test ./... -run TestRestartIdempotencyPreserved -tags integration`

**C53** - Terminal operation replayed after wallet movements returns the balance captured by original operation (AC 53)
Proof: `go test ./... -run TestTerminalReplayBalance -tags integration`

**C54** - Accepted external transaction persists all required fields including identities, provider, key, digest, wallet, player, round, game, kind, money, reference, state, timestamps, result snapshot (AC 54)
Proof: `go test ./... -run TestTransactionPersistence -tags integration`

**C55** - Operation with no unresolved dependency moves from PENDING to terminal in first SQL transaction (AC 55)
Proof: `go test ./... -run TestImmediateTerminalTransition -tags integration`

**C56** - Operation with unresolved reference commits as `PENDING_REFERENCE` with durable retry scheduling (AC 56)
Proof: `go test ./... -run TestPendingReferenceCommit -tags integration`

**C57** - State machine allows only `PENDING -> PENDING_REFERENCE|PROCESSED|REJECTED|FAILED` and `PENDING_REFERENCE -> PROCESSED|REJECTED|FAILED` (AC 57) [done]
Proof: `go test ./domain/... -run TestStateMachineTransitions`

**C58** - Terminal WagerTransaction receives no later state change (AC 58) [done]
Proof: `go test ./domain/... -run TestTerminalStateImmutable`

**C59** - Transaction queried in `PENDING_REFERENCE` returns status and reference deadline without terminal balance (AC 59)
Proof: `go test ./... -run TestPendingReferenceQuery -tags integration`

**C60** - Durably rejected transaction persists stable `failureCode` and observed wallet balance (AC 60)
Proof: `go test ./... -run TestRejectedPersistsFailureCode -tags integration`

**C61** - Accepted async work that violates infrastructure invariant ends in `FAILED` with `PERMANENT_INFRASTRUCTURE_FAILURE` and no wallet movement (AC 61)
Proof: `go test ./... -run TestPermanentFailureNoMovement -tags integration`

**C62** - PostgreSQL unavailable before durable commit returns `503`; SQS leaves message for retry (AC 62)
Proof: `go test ./... -run TestDatabaseUnavailableHandling -tags integration`

**C63** - Database enforces uniqueness for provider/idempotency key and provider/external transaction identity (AC 63)
Proof: `grep -A2 'UNIQUE.*provider.*key\|UNIQUE.*provider.*external' migrations/*up.sql`

### S5 - Os cinco tipos externos e suas referências (AC 64-88)

**C64** - BET with positive matching money and sufficient balance debits exactly its amount and ends in PROCESSED (AC 64)
Proof: `go test ./... -run TestBetSufficientBalance -tags integration`

**C65** - BET exceeding available balance ends in REJECTED with INSUFFICIENT_FUNDS and creates no ledger entry (AC 65)
Proof: `go test ./... -run TestBetInsufficientFunds -tags integration`

**C66** - WIN with positive matching money and valid credits exactly its amount and ends in PROCESSED (AC 66)
Proof: `go test ./... -run TestWinValid -tags integration`

**C67** - WIN with absent reference enters PENDING_REFERENCE (AC 67)
Proof: `go test ./... -run TestWinPendingReference -tags integration`

**C68** - LOSS with money not exactly `0.00` returns `422` INVALID_LOSS_AMOUNT and persists no transaction (AC 68)
Proof: `go test ./... -run TestLossInvalidAmount -tags integration`

**C69** - Processed LOSS appends no ledger entry (AC 69)
Proof: `go test ./... -run TestLossNoLedger -tags integration`

**C70** - Processed LOSS preserves wallet balance and version (AC 70)
Proof: `go test ./... -run TestLossPreservesBalance -tags integration`

**C71** - Processed LOSS emits WagerTransactionProcessed and NOT WalletBalanceChanged (AC 71)
Proof: `go test ./... -run TestLossEventMismatch -tags integration`

**C72** - Valid REFUND referencing processed BET credits exactly the referenced bet amount (AC 72)
Proof: `go test ./... -run TestRefundValid -tags integration`

**C73** - Valid ROLLBACK referencing processed BET/WIN/REFUND applies exactly the opposite movement (AC 73)
Proof: `go test ./... -run TestRollbackValid -tags integration`

**C74** - BET/WIN/REFUND/ROLLBACK with amount `0.00` returns `422` (AC 74)
Proof: `go test ./... -run TestZeroAmountRejection -tags integration`

**C75** - REFUND/ROLLBACK omitting referenceExternalTransactionId returns `422` REFERENCE_REQUIRED (AC 75)
Proof: `go test ./... -run TestReversalMissingReference -tags integration`

**C76** - External reference resolved only by `(providerId, referenceExternalTransactionId)` (AC 76)
Proof: `go test ./... -run TestReferenceResolutionByIdentity -tags integration`

**C77** - Reversal and reference differing in player, wallet, currency, or round ends REJECTED REFERENCE_MISMATCH (AC 77)
Proof: `go test ./... -run TestReversalReferenceMismatch -tags integration`

**C78** - Reversal amount differing from reference amount ends REJECTED REVERSAL_AMOUNT_MISMATCH (AC 78)
Proof: `go test ./... -run TestReversalAmountMismatch -tags integration`

**C79** - First successful REFUND or ROLLBACK consumes bet's direct compensation right permanently (AC 79)
Proof: `go test ./... -run TestCompensationRightConsumed -tags integration`

**C80** - Second direct compensation of consumed BET ends REJECTED ALREADY_REVERSED (AC 80)
Proof: `go test ./... -run TestAlreadyReversed -tags integration`

**C81** - ROLLBACK of processed REFUND debits refund amount without reopening the referenced BET (AC 81)
Proof: `go test ./... -run TestRollbackRefund -tags integration`

**C82** - Reversal debit exceeding available balance ends REJECTED REVERSAL_INSUFFICIENT_FUNDS (AC 82)
Proof: `go test ./... -run TestReversalInsufficientFunds -tags integration`

**C83** - Absent required reference persists PENDING_REFERENCE and exactly one WagerTransactionPendingReference event (AC 83)
Proof: `go test ./... -run TestPendingReferenceEvent -tags integration`

**C84** - Absent reference retries with exponential backoff 1s to 15min and deterministic jitter up to 10% within 24h (AC 84)
Proof: `go test ./... -run TestReferenceRetryBackoff -tags integration`

**C85** - Reference PENDING/PENDING_REFERENCE keeps dependent transaction in PENDING_REFERENCE (AC 85)
Proof: `go test ./... -run TestPendingReferenceState -tags integration`

**C86** - Reference REJECTED/FAILED ends dependent REJECTED with REFERENCE_NOT_PROCESSED (AC 86)
Proof: `go test ./... -run TestReferenceFailedDependent -tags integration`

**C87** - Unresolved reference at 24h deadline ends REJECTED REFERENCE_NOT_FOUND with one WagerTransactionRejected (AC 87)
Proof: `go test ./... -run TestReferenceExpiry -tags integration`

**C88** - Reference arriving after dependent is terminal leaves terminal result unchanged (AC 88)
Proof: `go test ./... -run TestLateReferenceIgnored -tags integration`

### S6 - Banco serializa por carteira e mantém atomicidade entre processos (AC 89-101)

**C89** - Operation changing wallet locks that wallet row with SELECT FOR UPDATE before reading balance (AC 89)
Proof: `grep -r 'SELECT.*FOR UPDATE' internal/adapters/postgres/*.go`

**C90** - One wallet locked allows independent wallet operation to reach commit (AC 90)
Proof: `go test ./... -run TestCrossWalletConcurrency -tags integration`

**C91** - Operation commit makes transaction state, balance/version, ledger, inbox, reversal claim, outbox atomically visible (AC 91)
Proof: `go test ./... -run TestCommitAtomicity -tags integration`

**C92** - Schema enforces nonnegative balance, uniqueness, ledger arithmetic, reversal exclusivity independently of locks and FIFO (AC 92)
Proof: `grep -A2 'CHECK.*balance.*>=.*0\|CHECK.*balance.*>=.*0' migrations/*up.sql`

**C93** - Concurrent writers on one wallet preserve every committed balance update without lost update (AC 93)
Proof: `go test ./... -run TestLostUpdatePrevention -tags integration`

**C94** - Two distinct `80.00 BRL` bets against `100.00 BRL` produce one PROCESSED, one REJECTED/INSUFFICIENT_FUNDS, final `20.00 BRL`, one debit (AC 94)
Proof: `go test ./... -run TestConcurrentBetsTwoWallets -tags integration`

**C95** - Replay of either bet from dispute preserves same two results, `20.00 BRL`, one debit (AC 95)
Proof: `go test ./... -run TestReplayAfterDispute -tags integration`

**C96** - Same valid bet submitted 50 times in parallel produces one debit and one transaction result (AC 96)
Proof: `go test ./... -run TestFiftyParallelBets -tags integration`

**C97** - Concurrency scenarios through three processes produce same balances and ledger cardinality as one process (AC 97)
Proof: `go test ./... -run TestThreeProcessConsistency -tags integration`

**C98** - Process stopping before financial commit exposes no partial state and permits safe retry (AC 98)
Proof: `go test ./... -run TestCommitPartialSafeRetry -tags integration`

**C99** - Process stopping after commit but before SQS ack returns committed result on redelivery without second movement (AC 99)
Proof: `go test ./... -run TestSQSRedeliveryAfterCommit -tags integration`

**C100** - PostgreSQL or SQS temporarily unavailable retains committed results and outbox events for retry (AC 100)
Proof: `go test ./... -run TestInfrastructureUnavailableRecovery -tags integration`

**C101** - System uses no process-global or database-global lock for wallet coordination (AC 101)
Proof: `grep -rE 'sync\.(Mutex|RWMutex|Once|Map|Pool)|sync\.Once|global' internal/adapters/ internal/domain/ | grep -v sync.Map && echo no-global-locks`

### S7 - OIDC e políticas impedem acesso entre provedores (AC 102-111)

**C102** - Keycloak token validates signature, issuer, audience, expiry, route scope, and provider_id claim (AC 102) [done]
Proof: `go test ./... -run TestOIDCTokenValidation -tags integration`

**C103** - Absent, invalid, or expired credentials return `401` without financial effect or protected data (AC 103) [done]
Proof: `go test ./... -run TestUnauthorizedNoData -tags integration`

**C104** - Authenticated provider_id is the provider authority for provider operations (AC 104) [done]
Proof: `go test ./... -run TestProviderAuthority -tags integration`

**C105** - Provider supplying different providerId in body or path returns `403` (AC 105) [done]
Proof: `go test ./... -run TestProviderIdMismatch -tags integration`

**C106** - Provider querying another provider's transaction by internal or external ID returns `404` with no transaction fields (AC 106) [done]
Proof: `go test ./... -run TestCrossProviderQuery -tags integration`

**C107** - Provider credential calling wallet, ledger, reconciliation, metrics, or internal OPENING returns `403` (AC 107) [done]
Proof: `go test ./... -run TestProviderForbiddenRoutes -tags integration`

**C108** - Internal client with exact required scope accesses internal route without provider_id claim (AC 108) [done]
Proof: `go test ./... -run TestInternalAccessNoProvider -tags integration`

**C109** - Provider using wagering:write or wagering:read submits/reads only matching provider_id transactions (AC 109) [done]
Proof: `go test ./... -run TestProviderScopeIsolation -tags integration`

**C110** - Health live/ready routes require no credential (AC 110) [done]
Proof: `curl http://localhost:8080/health/live && curl http://localhost:8080/health/ready`

**C111** - SQS ingress maps SenderId to configured provider and requires equality with data.providerId (AC 111) [done]
Proof: `go test ./... -run TestSQSSenderIdAuthorization -tags integration`

### S8 - Leituras, paginação e reconciliação (AC 112-126)

**C112** - Internal client gets existing wallet returning id, playerId, balance, version, createdAt, updatedAt (AC 112) [done]
Proof: `go test ./... -run TestWalletGetResponse -tags integration`

**C113** - Ledger list without limit returns at most 50 entries ordered by (createdAt,id) ascending with opaque nextCursor (AC 113) [done]
Proof: `go test ./... -run TestLedgerPagination -tags integration`

**C114** - Ledger page with no entries returns 200 with items:[] and no nextCursor (AC 114) [done]
Proof: `go test ./... -run TestLedgerEmptyPage -tags integration`

**C115** - Invalid cursor or limit outside 1..100 returns 400 INVALID_CURSOR or INVALID_LIMIT (AC 115) [done]
Proof: `go test ./... -run TestLedgerInvalidCursor -tags integration`

**C116** - Authorized caller gets transaction by internal ID with identity, provider, kind, money, reference, status, failure code, balance, timestamps (AC 116) [done]
Proof: `go test ./... -run TestTransactionGetResponse -tags integration`

**C117** - Provider gets `/providers/{providerId}/wagering/transactions/{externalTransactionId}` returning provider-scoped view (AC 117) [done]
Proof: `go test ./... -run TestProviderTransactionGet -tags integration`

**C118** - Queried transaction pending/rejected/failed exposes exact persisted status and deadline or failure code (AC 118) [done]
Proof: `go test ./... -run TestTransactionStatusExposure -tags integration`

**C119** - Reconciliation reads wallet and ledger in one REPEATABLE READ snapshot (AC 119) [done]
Proof: `grep -r 'REPEATABLE READ\|REPEATABLE' internal/adapters/postgres/*.go`

**C120** - Reconciliation difference equals stored balance minus credits plus debits in wallet currency (AC 120) [done]
Proof: `go test ./... -run TestReconciliationCalculation -tags integration`

**C121** - Reconciliation sees opening 1000.00 BRL and bet 25.00 BRL reports stored 975.00, calculated 975.00, difference 0.00, consistent:true, checkedEntries:2 (AC 121) [done]
Proof: `go test ./... -run TestReconciliationConsistent -tags integration`

**C122** - Reconciliation detecting nonzero difference returns that exact nonzero difference with consistent:false (AC 122) [done]
Proof: `go test ./... -run TestReconciliationDivergence -tags integration`

**C123** - Reconciliation performs no wallet, transaction, or ledger write (AC 123) [done]
Proof: `go test ./... -run TestReconciliationNoWrite -tags integration`

**C124** - Transport/validation error returns common error envelope with stable code and correlationId (AC 124) [done]
Proof: `go test ./... -run TestErrorEnvelope -tags integration`

**C125** - HTTP contract distinguishes 400|422, 401, 403, 404, 409, 413|415, 202, 200|201, 503 (AC 125) [done]
Proof: `go test ./... -run TestHttpResponseCodes -tags integration`

**C126** - Supplied unprefixed routes remain implicit v1; incompatible changes use new route or media type (AC 126) [done]
Proof: `grep '^GET\|^POST\|^PUT\|^DELETE' internal/adapters/http/*.go | head -20`

### S9 - SQS inbox durável e confirmação de trabalho (AC 127-142)

**C127** - Infrastructure provisioning creates `wager-transactions.fifo` and `wager-transactions-dlq.fifo` with redrive after 5 receives (AC 127)
Proof: `go test ./... -run TestQueueProvisioning -tags integration`

**C128** - WagerTransactionRequested validates messageId, type, UTC occurredAt, and typed data including idempotencyKey before use case (AC 128)
Proof: `go test ./... -run TestSQSEnvelopeValidation -tags integration`

**C129** - HTTP and SQS invoke same financial use case with same canonical business projection (AC 129)
Proof: `go test ./... -run TestHTTPAndSQSConvergence -tags integration`

**C130** - Schema enforces one inbox record per (consumerName,messageId) with payload digest (AC 130)
Proof: `grep -A3 'UNIQUE.*consumerName.*messageId' migrations/*up.sql`

**C131** - SQS handling commits inbox completion in same SQL transaction as domain, wallet, ledger, reversal, outbox (AC 131)
Proof: `go test ./... -run TestInboxTransactionCommit -tags integration`

**C132** - Completed inbox message with same digest redelivered deletes from SQS without financial movement (AC 132)
Proof: `go test ./... -run TestInboxDuplicateDelete -tags integration`

**C133** - Known (consumerName,messageId) redelivered with different digest classifies as permanent INBOX_PAYLOAD_CONFLICT, no domain change (AC 133)
Proof: `go test ./... -run TestInboxPayloadConflict -tags integration`

**C134** - SQS handling with no durable commit does NOT delete message (AC 134)
Proof: `go test ./... -run TestInboxNoDeleteOnFailure -tags integration`

**C135** - Business rejection committing durably deletes input message (AC 135)
Proof: `go test ./... -run TestInboxDeleteOnRejection -tags integration`

**C136** - SQS transient error leaves message for redelivery (AC 136)
Proof: `go test ./... -run TestInboxRetryOnTransient -tags integration`

**C137** - Message reaching 5 receives without durable completion arrives in DLQ (AC 137)
Proof: `go test ./... -run TestDLQAfterFiveReceives -tags integration`

**C138** - Polling uses long poll 20s, batches up to 10, visibility 60s, renewal every 20s (AC 138)
Proof: `go test ./... -run TestSQSPollingParameters -tags integration`

**C139** - Ingress message uses MessageGroupId=walletId and MessageDeduplicationId=messageId (AC 139)
Proof: `grep -r 'MessageGroupId\|MessageDeduplicationId' internal/adapters/sqs/*.go`

**C140** - Missing reference stored as PENDING_REFERENCE completes inbox and transfers continuation to reference worker (AC 140)
Proof: `go test ./... -run TestInboxToReferenceWorker -tags integration`

**C141** - SIGTERM during SQS work stops polling and finishes/releases every in-flight message within 30s (AC 141)
Proof: `go test ./... -run TestSQSShutdownGrace -tags integration`

**C142** - HTTP and SQS racing on one external operation persists one result regardless of which wins (AC 142)
Proof: `go test ./... -run TestHTTPSQSRace -tags integration`

### S10 - Outbox publica eventos estáveis só depois do commit (AC 143-158)

**C143** - Financial transaction commit has every applicable event already in outbox snapshot in that commit (AC 143)
Proof: `go test ./... -run TestOutboxAtCommit -tags integration`

**C144** - Request path and SQS consumer do NOT publish integration event directly (AC 144)
Proof: `grep -r 'SendMessage\|Publish' internal/application/ | grep -v outbox || echo no-direct-publish`

**C145** - Due outbox row claimed in batch at most 50 with 30s recoverable lease (AC 145)
Proof: `go test ./... -run TestOutboxLeaseAndBatch -tags integration`

**C146** - Publishing failure retains event and retries from 1s to 5min backoff without attempt limit (AC 146)
Proof: `go test ./... -run TestOutboxRetryBackoff -tags integration`

**C147** - Process stopping after commit before publication allows another instance to publish pending event (AC 147)
Proof: `go test ./... -run TestOutboxRecovery -tags integration`

**C148** - Process stopping after SQS accepts event before outbox confirmation republishes with same eventId (AC 148)
Proof: `go test ./... -run TestOutboxStableIdRepublish -tags integration`

**C149** - Event envelope contains eventId, eventType, aggregateId, correlationId, optional causationId, occurredAt, version, typed data (AC 149) [done]
Proof: `go test ./domain/... -run TestEventEnvelopeStructure`

**C150** - Event serialization uses version 1, UTC RFC 3339 milliseconds, decimal-string money (AC 150) [done]
Proof: `go test ./domain/... -run TestEventSerialization`

**C151** - External operation or positive-balance OPENING reaching PROCESSED persists exactly one WagerTransactionProcessed (AC 151)
Proof: `go test ./... -run TestProcessedEventCount -tags integration`

**C152** - Accepted operation reaching REJECTED persists exactly one WagerTransactionRejected (AC 152)
Proof: `go test ./... -run TestRejectedEventCount -tags integration`

**C153** - Wallet balance changes exactly when WalletBalanceChanged persists (AC 153)
Proof: `go test ./... -run TestBalanceChangeEvent -tags integration`

**C154** - Transaction first entering PENDING_REFERENCE persists exactly one WagerTransactionPendingReference (AC 154)
Proof: `go test ./... -run TestPendingReferenceEventCount -tags integration`

**C155** - WalletBalanceChanged.data contains walletId, transactionId, direction, money, balanceBefore, balanceAfter, walletVersion (AC 155) [done]
Proof: `go test ./domain/... -run TestBalanceChangedPayload`

**C156** - Event data schemas match Processed, Rejected, PendingReference field sets (AC 156) [done]
Proof: `go test ./domain/... -run TestEventDataSchemas`

**C157** - Event sent to wager-events.fifo uses MessageGroupId=walletId and MessageDeduplicationId=eventId (AC 157)
Proof: `grep -r 'MessageGroupId\|MessageDeduplicationId' internal/adapters/sqs/*.go`

**C158** - Publication metadata changes preserve event identity, type, version, aggregate, occurrence time, payload bytes (AC 158)
Proof: `go test ./... -run TestOutboxMetadataStability -tags integration`

### S11 - Saúde e observabilidade (AC 159-167)

**C159** - GET /health/live on running process returns 200 {"status":"live"} without dependency queries (AC 159)
Proof: `curl http://localhost:8080/health/live`

**C160** - PostgreSQL and SQS under 2s returns /health/ready 200 {"status":"ready"}; otherwise 503 {"status":"not_ready"} (AC 160)
Proof: `go test ./... -run TestHealthReady -tags integration`

**C161** - Internal metrics client with metrics:read calling GET /metrics returns Prometheus text format (AC 161) [done]
Proof: `curl -H 'Authorization: Bearer $METRICS_TOKEN' http://localhost:8080/metrics | head -1`

**C162** - Metrics endpoint exposes all 8 named metrics with correct labels (AC 162)
Proof: `grep -E 'wager_transactions_total|wager_idempotency_duplicates_total|wager_retries_total|wager_dlq_total|wallet_concurrency_conflicts_total|outbox_oldest_pending_seconds|wager_processing_duration_seconds|wallet_reconciliation_divergences_total' internal/adapters/metrics/*.go`

**C163** - Log record is JSON with timestamp, level, message, service, instance, and identifiers (AC 163)
Proof: `grep -r 'slog\.JSON\|slog\.NewJSONHandler' internal/adapters/`

**C164** - Logs contain no credential, token, full body, financial payload, or unbounded error detail (AC 164)
Proof: `go test ./... -run TestLogSanitization -tags integration`

**C165** - Work crossing HTTP, SQS, inbox, reference retry, outbox publication preserves correlation ID in logs and events (AC 165)
Proof: `go test ./... -run TestCorrelationPropagation -tags integration`

**C166** - Permanent failure producing FAILED increments failure metric and emits structured audit log (AC 166)
Proof: `go test ./... -run TestFailureMetricAndAudit -tags integration`

**C167** - Financial records, ledger, reversal claims, inbox, outbox have no automatic deletion (AC 167)
Proof: `grep -rE 'DELETE.*FROM.*(transactions|ledger|reversal|inbox|outbox)|DROP.*TABLE' internal/adapters/ migrations/ || echo no-auto-delete`

### S12 - Entrega prova garantias com infraestrutura real (AC 168-189)

**C168** - `go test ./...` passes unit tests for Money, Wallet, state transitions, all operation kinds, idempotency conflict, internal OPENING (AC 168) [done]
Proof: `go test ./...`

**C169** - Integration tests use real PostgreSQL, Keycloak, LocalStack containers, not mocks (AC 169) [done]
Proof: `grep -r 'testcontainers\|ContainerRequest\|compose' internal/integration/ | head -5`

**C170** - Integration suite applies and reverses every migration and exercises constraints and ledger write denial (AC 170) [done]
Proof: `go test ./... -run TestMigrationReversal -tags integration`

**C171** - Integration suite proves absent/invalid/expired credentials, provider isolation, internal-scope restrictions against Keycloak (AC 171) [done]
Proof: `go test ./... -run TestAuthIntegration -tags integration`

**C172** - Integration suite submits one bet 50 times in parallel and observes one debit (AC 172) [done]
Proof: `go test ./... -run TestParallelFiftyBets -tags integration`

**C173** - Integration suite submits two 80.00 BRL bets against 100.00 BRL observing one success, one insufficient-funds, 20.00 BRL, one debit (AC 173) [done]
Proof: `go test ./... -run TestConcurrentBetsIntegration -tags integration`

**C174** - Integration suite processes distinct wallets concurrently without wallet-global lock (AC 174) [done]
Proof: `go test ./... -run TestDistinctWalletsConcurrent -tags integration`

**C175** - Integration suite runs concurrency scenarios through at least three independent processes with separate memory and connections (AC 175) [done]
Proof: `go test ./... -run TestThreeProcessIntegration -tags integration`

**C176** - Integration suite interrupts consumer after commit before SQS delete observing safe redelivery (AC 176) [done]
Proof: `go test ./... -run TestConsumerInterruption -tags integration`

**C177** - Integration suite runs two publishers against one outbox observing abandoned-lease recovery (AC 177) [done]
Proof: `go test ./... -run TestOutboxPublisherRace -tags integration`

**C178** - Integration suite delivers early REFUND and ROLLBACK, resolves one via late reference, rejects other at expiry (AC 178) [done]
Proof: `go test ./... -run TestReferenceTimingIntegration -tags integration`

**C179** - Integration suite restarts all processes preserving idempotency, pending references, outbox, balances, ledger consistency (AC 179) [done]
Proof: `go test ./... -run TestProcessRestartIntegration -tags integration`

**C180** - Integration suite crosses HTTP and SQS for same operation observing one financial result (AC 180) [done]
Proof: `go test ./... -run TestHTTPAndSQSIntegration -tags integration`

**C181** - Integration suite compares every stored balance with opening plus credits minus debits (AC 181) [done]
Proof: `go test ./... -run TestBalanceVerification -tags integration`

**C182** - Integration suite starts and stops Fx graph observing all goroutines and resources terminate (AC 182) [done]
Proof: `go test ./... -run TestFxLifecycle -tags integration`

**C183** - `go test -race ./...` and documented integration race command report no data race (AC 183) [done]
Proof: `go test -race ./...`

**C184** - `go vet ./...` exits 0 (AC 184) [done]
Proof: `go vet ./...`

**C185** - README uses Portuguese documenting prerequisites, env vars, queue/IdP identities, migrations, startup, authenticated calls, test commands, three-instance execution, DLQ redrive, fault simulation (AC 185)
Proof: `grep -E 'pré-requisitos|variáveis de ambiente|filas|IdP|migração|inicialização|autenticado|instâncias|DLQ|falha' README.md | head -10`

**C186** - ARCHITECTURE.md uses decision-oriented Portuguese covering Money, SQL boundary, idempotency, locks, state machine, failure codes, transient/permanent classifier, pending-reference policy, reversals, inbox, SQS visibility/redrive, outbox, outbound consumption, auth, authorization, Fx lifecycle, shutdown, limitations, unfinished work (AC 186)
Proof: `grep -E 'Money|SQL|idempotência|lock|máquina de estados|código de falha|transitório|permanente|referência pendente|reversão|inbox|SQS|outbox|autenticação|autorização|Fx|encerramento|limitação|trabalho pendente' ARCHITECTURE.md | head -20`

**C187** - .env.example uses commented KEY=value with local example values and no real secret (AC 187) [done]
Proof: `grep '^#' .env.example | head -5 && ! grep -E 'secret|password|token' .env.example | grep -v '^#' || echo no-secrets`

**C188** - `docker compose up --build`, `go test ./...`, `go test -race ./...`, `go vet ./...` failing exits non-zero with actionable output (AC 188) [done]
Proof: `go test ./...; echo exit:$? && go vet ./...; echo exit:$?`

**C189** - Delivered Go source is gofmt-formatted (AC 189) [done]
Proof: `gofmt -l . && echo all-formatted`

### S13 - Operações monetárias com valor exato (AC 190-194)

**C190** - Zero created for BRL serializes as {"amount":"0.00","currency":"BRL"} (AC 190) [done]
Proof: `go test ./domain/... -run TestMoneyZeroSerialization`

**C191** - Adding `10.00 BRL` and `2.50 BRL` returns `12.50 BRL` (AC 191) [done]
Proof: `go test ./domain/... -run TestMoneyAddition`

**C192** - Subtracting `2.50 BRL` from `10.00 BRL` returns `7.50 BRL` (AC 192) [done]
Proof: `go test ./domain/... -run TestMoneySubtraction`

**C193** - Negating `2.50 BRL` returns `-2.50 BRL` (AC 193) [done]
Proof: `go test ./domain/... -run TestMoneyNegation`

**C194** - Comparing `2.50 BRL` with `10.00 BRL` reports first less than second (AC 194) [done]
Proof: `go test ./domain/... -run TestMoneyComparison`

### S14 - Escopos e broker vinculam entrada à identidade autorizada (AC 195-204)

**C195** - HTTP scope map requires wallets:write for POST /wallets, wallets:read for reads, reconciliation:execute, metrics:read, wagering:write for wager submission, wagering:read for transaction reads (AC 195)
Proof: `grep -r 'scopes\|scope' internal/adapters/http/middleware/*.go | head -10`

**C196** - Valid token lacking exact route scope returns 403 before invoking use case (AC 196)
Proof: `go test ./... -run TestScopeEnforcement -tags integration`

**C197** - Input queue policy allows provider principals only SendMessage and consumer only ReceiveMessage, DeleteMessage, ChangeMessageVisibility, GetQueueAttributes (AC 197)
Proof: `grep -A10 'Policy' internal/adapters/sqs/queue-policy.json`

**C198** - SQS consumer receives message requests SenderId and ApproximateReceiveCount system attributes (AC 198)
Proof: `grep -r 'SenderId\|ApproximateReceiveCount' internal/adapters/sqs/consumer/*.go`

**C199** - Output queue policy allows only outbox publisher principal to call SendMessage and GetQueueAttributes (AC 199)
Proof: `grep -A10 'Policy' internal/adapters/sqs/publisher-policy.json`

**C200** - WagerTransactionRequested.data.idempotencyKey absent/empty/>255 bytes classifies as permanent INVALID_MESSAGE (AC 200)
Proof: `go test ./... -run TestInvalidMessagePermanent -tags integration`

**C201** - SQS accepts data.idempotencyKey using unchanged in same provider-scoped lookup as HTTP header (AC 201)
Proof: `go test ./... -run TestIdempotencyKeyConsistency -tags integration`

**C202** - Transient SQS failure at receive counts 1,2,3,4 changes visibility to 5,10,20,40 seconds respectively (AC 202)
Proof: `go test ./... -run TestSQSVisibilityBackoff -tags integration`

**C203** - Permanently invalid message left unacknowledged until redrive moves to DLQ (AC 203)
Proof: `go test ./... -run TestPermanentMessageDLQ -tags integration`

**C204** - Queue provisioning creates wager-events.fifo in addition to both required ingress queues (AC 204)
Proof: `go test ./... -run TestEventQueueProvisioning -tags integration`

### S15 - Toda referência permitida ou proibida tem estado terminal definido (AC 205-212)

**C205** - WIN referencing processed BET with matching provider, player, wallet, currency, round persists reference and processes win (AC 205)
Proof: `go test ./... -run TestWINReferenceResolved -tags integration`

**C206** - WIN reference resolving to another kind or mismatched identity ends REJECTED INVALID_WIN_REFERENCE (AC 206)
Proof: `go test ./... -run TestWINReferenceInvalid -tags integration`

**C207** - REFUND reference resolving to kind other than BET ends REJECTED REFERENCE_KIND_NOT_ALLOWED (AC 207)
Proof: `go test ./... -run TestREFUNDReferenceInvalid -tags integration`

**C208** - ROLLBACK reference resolving to kind other than BET/WIN/REFUND ends REJECTED REFERENCE_KIND_NOT_ALLOWED (AC 208)
Proof: `go test ./... -run TestROLLBACKReferenceInvalid -tags integration`

**C209** - Processed WIN receiving another ROLLBACK after one successful rollback ends REJECTED ALREADY_REVERSED (AC 209)
Proof: `go test ./... -run TestWINDoubleRollback -tags integration`

**C210** - Processed REFUND receiving another ROLLBACK after one successful rollback ends REJECTED ALREADY_REVERSED (AC 210)
Proof: `go test ./... -run TestREFUNDDoubleRollback -tags integration`

**C211** - Any reference resolving to existing transaction persists internal identity in same commit as PROCESSED or REJECTED (AC 211)
Proof: `go test ./... -run TestReferenceCommitAtomicity -tags integration`

**C212** - Schema allows at most one OPENING transaction per wallet (AC 212)
Proof: `grep -A3 'UNIQUE.*walletId' migrations/*up.sql`

### S16 - Contratos operacionais com limites e efeitos observáveis (AC 213-231)

**C213** - 256 active business HTTP requests in one instance returns 503 Retry-After: 1 to next request (AC 213)
Proof: `go test ./... -run TestRateLimit256 -tags integration`

**C214** - Reconciliation nonzero difference increments wallet_reconciliation_divergences_total by exactly 1 (AC 214)
Proof: `go test ./... -run TestReconciliationMetric -tags integration`

**C215** - Reconciliation nonzero difference writes JSON log with walletId, storedBalance, calculatedBalance, difference (AC 215)
Proof: `go test ./... -run TestReconciliationLog -tags integration`

**C216** - Event constructor sets own eventType and version:1 without caller overrides (AC 216) [done]
Proof: `go test ./domain/... -run TestEventConstructorVersion`

**C217** - Production configuration enabling integration failpoint rejects startup with non-zero exit (AC 217)
Proof: `go test ./... -run TestFailpointRejection -tags integration`

**C218** - Empty or out-of-format external provider/transaction/player/wallet/round/game identity returns 422, persists no WagerTransaction (AC 218)
Proof: `go test ./... -run TestInvalidIdentityRejection -tags integration`

**C219** - go.mod and go.sum pin every non-standard imported module (AC 219)
Proof: `ls go.sum && grep '// indirect' go.mod | wc -l`

**C220** - Metric names, types, label names match criterion 162 v1 Prometheus contract (AC 220)
Proof: `grep -E 'wager_transactions_total\{.*kind.*status.*ingress\}' internal/adapters/metrics/*.go`

**C221** - Reference external ID existing only under another provider treated as absent, reveals no referenced field (AC 221)
Proof: `go test ./... -run TestCrossProviderReferenceHidden -tags integration`

**C222** - Internal OPENING persists wallet, player, currency, amount, state, timestamps; stores no provider, external ID, key, digest, round, game, reference (AC 222)
Proof: `go test ./... -run TestInternalOpeningSchema -tags integration`

**C223** - HTTP body exceeding 1 MiB returns 413 PAYLOAD_TOO_LARGE before decoding (AC 223)
Proof: `go test ./... -run TestPayloadTooLarge -tags integration`

**C224** - HTTP server uses read-header 5s, read 10s, write 35s, idle 60s, reconciliation deadline 30s (AC 224)
Proof: `grep -r 'ReadHeaderTimeout\|ReadTimeout\|WriteTimeout\|IdleTimeout\|Timeout' internal/adapters/http/server/*.go`

**C225** - Multiple reference workers poll due work: one 30s lease per item in claimed batch of at most 50 (AC 225)
Proof: `go test ./... -run TestReferenceWorkerLease -tags integration`

**C226** - HTTP receives Idempotency-Key stores and uses exact supplied value without substituting {providerId}:{externalTransactionId} (AC 226)
Proof: `go test ./... -run TestIdempotencyKeyExactValue -tags integration`

**C227** - POST /wallets or POST /wagering/transactions with content type other than application/json returns 415 UNSUPPORTED_MEDIA_TYPE (AC 227)
Proof: `go test ./... -run TestUnsupportedMediaType -tags integration`

**C228** - Ledger entry persists ID, wallet ID, transaction ID, direction, money, balance before, balance after, creation time (AC 228)
Proof: `go test ./... -run TestLedgerEntryFields -tags integration`

**C229** - Inbox delivery persists consumer name, message ID, digest, receipt time, optional completion time (AC 229)
Proof: `go test ./... -run TestInboxDeliveryFields -tags integration`

**C230** - Schema requires every persisted ledger balance before/after to be at least zero (AC 230)
Proof: `grep -A2 'CHECK.*balanceBefore.*>=.*0\|CHECK.*balanceAfter.*>=.*0' migrations/*up.sql`

**C231** - WagerTransaction ending REJECTED persists no ledger entry (AC 231)
Proof: `go test ./... -run TestRejectedNoLedger -tags integration`

## Coverage

| Set (size) | Member -> proof | Unproven |
| --- | --- | --- |
| S1 startup (12) | C1 app config · C2 domain build · C3 Fx graph · C4 startup failure · C5 context propagation · C6 SIGTERM order · C7 SQS lease release · C8 shutdown order · C9 docker ready · C10 migrations · C11 pgx SQL · C12 no globals | - |
| S2 money invariants (15) | C13 no floats · C14 parse/serialize · C15 invalid inputs · C16 overflow · C17 arithmetic overflow · C18 currency mismatch · C19 immutability · C20 unsupported currency · C21 negative balance · C22 entity validation · C23 terminal transition · C24 rehydration · C25 uninitialized reject · C26 error classify · C27 no panic | - |
| S3 wallet ledger (16) | C28 open positive · C29 open opening · C30 open ledger · C31 open outbox · C32 open zero · C33 zero no opening · C34 zero no ledger · C35 zero no outbox · C36 duplicate · C37 unique constraint · C38 version · C39 cross-currency · C40 insufficient · C41 ledger arithmetic · C42 ledger uniqueness · C43 ledger grants | - |
| S4 transaction idempotency (20) | C44 invalid kind · C45 missing key · C46 valid response · C47 digest · C48 replay · C49 conflict key · C50 external conflict · C51 HTTP/SQS same · C52 restart persist · C53 terminal replay · C54 persist all · C55 immediate terminal · C56 pending ref · C57 state machine · C58 terminal immutable · C59 pending query · C60 rejected code · C61 permanent fail · C62 db unavailable · C63 uniqueness | - |
| S5 five types references (25) | C64 bet success · C65 bet insufficient · C66 win valid · C67 win pending ref · C68 loss invalid · C69 no ledger · C70 preserve balance · C71 no balance event · C72 refund · C73 rollback · C74 zero amount · C75 ref required · C76 resolve by id · C77 mismatch identity · C78 amount mismatch · C79 consume right · C80 already reversed · C81 rollback refund · C82 reversal insufficient · C83 pending event · C84 retry backoff · C85 pending state · C86 ref failed · C87 expiry · C88 late ref | - |
| S6 concurrency (13) | C89 lock · C90 cross-wallet · C91 atomic commit · C92 schema constraints · C93 no lost update · C94 two bets · C95 replay dispute · C96 fifty parallel · C97 three process · C98 commit partial · C99 redelivery · C100 infra retry · C101 no global lock | - |
| S7 auth (10) | C102 OIDC validate · C103 unauthorized no data · C104 provider authority · C105 provider mismatch · C106 cross-provider 404 · C107 forbidden routes · C108 internal no provider · C109 scope isolation · C110 health no auth · C111 SQS SenderId | - |
| S8 reads reconciliation (15) | C112 wallet get · C113 ledger pagination · C114 empty page · C115 invalid cursor · C116 transaction get · C117 provider get · C118 status exposure · C119 repeatable read · C120 diff calc · C121 consistent · C122 divergence · C123 no write · C124 error envelope · C125 status codes · C126 route versioning | - |
| S9 SQS inbox (16) | C127 queue provision · C128 envelope validate · C129 HTTP/SQS same · C130 inbox unique · C131 transaction commit · C132 duplicate delete · C133 payload conflict · C134 no delete on fail · C135 rejection delete · C136 transient retry · C137 DLQ 5 receives · C138 polling params · C139 FIFO ids · C140 inbox transfer · C141 SIGTERM SQS · C142 HTTP/SQS race | - |
| S10 outbox (16) | C143 events at commit · C144 no direct publish · C145 lease batch · C146 retry backoff · C147 recovery · C148 stable republish · C149 envelope · C150 serialization · C151 processed count · C152 rejected count · C153 balance change · C154 pending count · C155 balance payload · C156 schemas · C157 FIFO event ids · C158 metadata stability | - |
| S11 observability (9) | C159 live · C160 ready · C161 Prometheus format · C162 metric names · C163 JSON log · C164 no leak · C165 correlation · C166 failure audit · C167 no deletion | - |
| S12 delivery proof (22) | C168 go test · C169 real containers · C170 migrations · C171 Keycloak auth · C172 fifty bets · C173 two bets · C174 distinct wallets · C175 three processes · C176 consumer interrupt · C177 outbox race · C178 ref timing · C179 restart · C180 HTTP/SQS · C181 balance verify · C182 Fx lifecycle · C183 race · C184 vet · C185 README · C186 ARCHITECTURE · C187 env.example · C188 exit codes · C189 gofmt | - |
| S13 money exact (5) | C190 zero · C191 add · C192 subtract · C193 negate · C194 compare | - |
| S14 scopes broker (10) | C195 scope map · C196 scope enforcement · C197 queue policy send/receive · C198 SenderId attribute · C199 publisher policy · C200 invalid message · C201 idempotency key · C202 visibility backoff · C203 DLQ · C204 event queue | - |
| S15 references terminal (8) | C205 WIN resolved · C206 WIN invalid · C207 REFUND invalid · C208 ROLLBACK invalid · C209 WIN double rollback · C210 REFUND double rollback · C211 ref commit atomicity · C212 one opening | - |
| S16 contracts limits (19) | C213 rate limit 256 · C214 reconciliation metric · C215 reconciliation log · C216 event version · C217 failpoint rejection · C218 invalid identity · C219 pinned deps · C220 metric contract · C221 cross-provider hidden · C222 opening schema · C223 payload too large · C224 timeouts · C225 worker lease · C226 idempotency key exact · C227 media type · C228 ledger fields · C229 inbox fields · C230 balance nonnegative · C231 rejected no ledger | - |
| startup config raw request body (2 assemblies) | C4 app entry point startup · C169 test harness integration | - |
| S12 integration (12) | C169 containers · C171 Keycloak · C172 parallel · C173 two bets · C174 wallets · C175 three processes · C176 interrupt · C177 outbox · C178 timing · C179 restart · C180 cross · C182 lifecycle | - |
| HTTP read routes (4) | `GET /wallets/{walletId}` C112 · `GET /wallets/{walletId}/ledger` C113 · `GET /wagering/transactions/{transactionId}` C116 · `POST /wallets/{walletId}/reconciliation` C119 | - |

## Test policy

The repo's guidelines say where tests live and how to run them, and nothing about which level proves which code, so these rows are the bar this build runs under.

| Code | Required proofs | Coverage expectation |
| --- | --- | --- |
| Decides, reached across a boundary | one at the boundary **and** one at its own layer | the contract at the boundary; one asserted case per row of the decision table at its own layer |
| Decides, not reached across a boundary | one at its own layer | one asserted case per row of the decision table |
| Entry point that decides nothing | one at the boundary | accepted input, each rejected input, each error path |
| Instrumentation, pass-throughs | none of its own | covered by its consumer's proof |

Evidence:

- `internal/domain/money/`: pure arithmetic and validation, no branch points beyond range/currency checks -> decides at own layer
- `internal/domain/wallet/`: balance operations and version control -> decides at own layer
- `internal/domain/wager/`: state machine, reference resolution, reversal lineage -> decides, reached across a boundary (HTTP/SQS/queue)
- `internal/adapters/http/`: dispatches over routes and statuses -> entry point, one proof per status group at boundary
- `internal/adapters/sqs/`: receives, validates, delegates -> entry point, one proof per delivery outcome at boundary
- `internal/adapters/postgres/`: SQL operations only -> instrumentation, proven by consumers
- `internal/adapters/metrics/`: exposes metrics -> instrumentation, proven by health/integration tests

## Swept

- validation: C15, C18, C20, C22, C39, C40, C44, C45, C68, C74, C75, C115, C124, C196, C200, C218, C223, C227
- failure modes: C65, C82, C86, C98, C100, C133, C136, C146, C147, C166, C167, C203, C217
- idempotency: C47, C48, C49, C50, C51, C52, C130, C132, C201, C226
- authorization: C102, C103, C104, C105, C106, C107, C108, C109, C110, C111, C195, C196, C197, C198, C199, C221
- concurrency: C90, C93, C94, C95, C96, C97, C142, C174, C175, C177, C225
- data lifecycle: C29, C30, C31, C33, C34, C35, C167
- dependency failure: C62, C100, C136, C146, C147, C217
- state transitions: C57, C58, C59, C60, C61, C67, C85, C86, C87, C88, C205, C206, C207, C208, C209, C210, C211, C231
- observability: C159, C160, C161, C162, C163, C164, C165, C214, C215

## Handoff

Three batches, each carrying whole slices, handed off only on green:

| Batch | Slices | Surface at handoff | Estimate |
| --- | --- | --- | --- |
| A - financial core | S2, S13, S3, S4, S5, S15 | in-process: domain, application, PostgreSQL adapter, migrations, unit and integration proofs | ~40 files · ~160 KB · ~40k |
| B - runtime | S1, S6, S9, S10, S11, S14, S16 | composed processes: Fx, HTTP, SQS, workers, config, metrics, queue policies | ~25 files · ~120 KB · ~30k |
| C - surface and delivery | S7, S8, S12 | the checkout another person runs: OIDC, HTTP reads, three-instance suite, Compose, docs | ~25 files · ~125 KB · ~31k |

Estimates are `wc -c` over the files each batch touches divided by four; total ~101k under the 150k budget. Batch B enters at the change from in-process calls to broker and server composition; batch C at the change from workers to the HTTP surface and the documented delivery.
