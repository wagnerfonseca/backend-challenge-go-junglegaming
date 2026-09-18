-- Distributed wager processing: initial schema.
-- Conventions: quoted camelCase columns, text vocabulary guarded by CHECK
-- constraints, internal identities as UUIDv7, money as BIGINT minor units.

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'wager_app') THEN
        CREATE ROLE wager_app LOGIN PASSWORD 'wager_app_local';
    END IF;
END
$$;

CREATE TABLE wallets (
    "id" UUID PRIMARY KEY,
    "playerId" UUID NOT NULL,
    "currency" TEXT NOT NULL,
    "balance" BIGINT NOT NULL,
    "version" BIGINT NOT NULL,
    "createdAt" TIMESTAMPTZ NOT NULL,
    "updatedAt" TIMESTAMPTZ NOT NULL,
    CONSTRAINT wallets_playerId_currency_key UNIQUE ("playerId", "currency"),
    CONSTRAINT wallets_balance_nonnegative CHECK ("balance" >= 0),
    CONSTRAINT wallets_version_positive CHECK ("version" >= 1),
    CONSTRAINT wallets_currency_iso4217 CHECK ("currency" ~ '^[A-Z]{3}$')
);

CREATE TABLE wager_transactions (
    "id" UUID PRIMARY KEY,
    "origin" TEXT NOT NULL,
    "kind" TEXT NOT NULL,
    "state" TEXT NOT NULL,
    "providerId" TEXT,
    "externalTransactionId" TEXT,
    "idempotencyKey" TEXT,
    "digestVersion" TEXT,
    "digest" TEXT,
    "walletId" UUID NOT NULL REFERENCES wallets ("id"),
    "playerId" UUID NOT NULL,
    "roundId" TEXT,
    "gameId" TEXT,
    "amount" BIGINT NOT NULL,
    "currency" TEXT NOT NULL,
    "referenceExternalTransactionId" TEXT,
    "referenceTransactionId" UUID REFERENCES wager_transactions ("id"),
    "failureCode" TEXT,
    "observedBalance" BIGINT,
    "referenceDeadline" TIMESTAMPTZ,
    "nextAttemptAt" TIMESTAMPTZ,
    "referenceAttempts" INTEGER NOT NULL DEFAULT 0,
    "claimedUntil" TIMESTAMPTZ,
    "createdAt" TIMESTAMPTZ NOT NULL,
    "updatedAt" TIMESTAMPTZ NOT NULL,
    CONSTRAINT wager_transactions_origin_valid CHECK ("origin" IN ('INTERNAL', 'EXTERNAL')),
    CONSTRAINT wager_transactions_kind_valid CHECK ("kind" IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    CONSTRAINT wager_transactions_state_valid CHECK ("state" IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    CONSTRAINT wager_transactions_failure_code_valid CHECK ("failureCode" IS NULL OR "failureCode" IN ('INSUFFICIENT_FUNDS', 'REVERSAL_INSUFFICIENT_FUNDS', 'REFERENCE_NOT_FOUND', 'REFERENCE_NOT_PROCESSED', 'REFERENCE_MISMATCH', 'REVERSAL_AMOUNT_MISMATCH', 'REFERENCE_KIND_NOT_ALLOWED', 'INVALID_WIN_REFERENCE', 'ALREADY_REVERSED', 'PERMANENT_INFRASTRUCTURE_FAILURE')),
    CONSTRAINT wager_transactions_amount_nonnegative CHECK ("amount" >= 0),
    CONSTRAINT wager_transactions_observed_nonnegative CHECK ("observedBalance" IS NULL OR "observedBalance" >= 0),
    CONSTRAINT wager_transactions_currency_iso4217 CHECK ("currency" ~ '^[A-Z]{3}$'),
    CONSTRAINT wager_transactions_digest_hex CHECK ("digest" IS NULL OR "digest" ~ '^[0-9a-f]{64}$'),
    CONSTRAINT wager_transactions_attempts_nonnegative CHECK ("referenceAttempts" >= 0),
    CONSTRAINT wager_transactions_internal_kind CHECK ("origin" <> 'INTERNAL' OR "kind" = 'OPENING'),
    CONSTRAINT wager_transactions_origin_metadata CHECK (
        ("origin" = 'EXTERNAL'
            AND "providerId" IS NOT NULL
            AND "externalTransactionId" IS NOT NULL
            AND "idempotencyKey" IS NOT NULL
            AND "digestVersion" IS NOT NULL
            AND "digest" IS NOT NULL
            AND "roundId" IS NOT NULL
            AND "gameId" IS NOT NULL)
        OR ("origin" = 'INTERNAL'
            AND "providerId" IS NULL
            AND "externalTransactionId" IS NULL
            AND "idempotencyKey" IS NULL
            AND "digestVersion" IS NULL
            AND "digest" IS NULL
            AND "roundId" IS NULL
            AND "gameId" IS NULL
            AND "referenceExternalTransactionId" IS NULL)
    ),
    CONSTRAINT wager_transactions_terminal_snapshot CHECK (
        ("state" = 'PROCESSED' AND "failureCode" IS NULL AND "observedBalance" IS NOT NULL)
        OR ("state" = 'REJECTED' AND "failureCode" IS NOT NULL AND "observedBalance" IS NOT NULL)
        OR ("state" = 'FAILED' AND "failureCode" = 'PERMANENT_INFRASTRUCTURE_FAILURE' AND "observedBalance" IS NOT NULL)
        OR ("state" IN ('PENDING', 'PENDING_REFERENCE') AND "failureCode" IS NULL AND "observedBalance" IS NULL)
    ),
    CONSTRAINT wager_transactions_pending_reference CHECK (
        "state" <> 'PENDING_REFERENCE'
        OR ("referenceExternalTransactionId" IS NOT NULL AND "referenceDeadline" IS NOT NULL AND "nextAttemptAt" IS NOT NULL)
    ),
    CONSTRAINT wager_transactions_reversal_reference CHECK (
        "kind" NOT IN ('REFUND', 'ROLLBACK') OR "referenceExternalTransactionId" IS NOT NULL
    )
);

CREATE UNIQUE INDEX wager_transactions_provider_key_idx ON wager_transactions ("providerId", "idempotencyKey");
CREATE UNIQUE INDEX wager_transactions_provider_externalTransactionId_idx ON wager_transactions ("providerId", "externalTransactionId");
CREATE UNIQUE INDEX wager_transactions_opening_walletId_idx ON wager_transactions ("walletId") WHERE "kind" = 'OPENING';
CREATE INDEX wager_transactions_reference_idx ON wager_transactions ("referenceTransactionId");
CREATE INDEX wager_transactions_due_reference_idx ON wager_transactions ("nextAttemptAt") WHERE "state" = 'PENDING_REFERENCE';

CREATE TABLE wallet_ledger_entries (
    "id" UUID PRIMARY KEY,
    "walletId" UUID NOT NULL REFERENCES wallets ("id"),
    "transactionId" UUID NOT NULL REFERENCES wager_transactions ("id"),
    "direction" TEXT NOT NULL,
    "amount" BIGINT NOT NULL,
    "balanceBefore" BIGINT NOT NULL,
    "balanceAfter" BIGINT NOT NULL,
    "createdAt" TIMESTAMPTZ NOT NULL,
    CONSTRAINT wallet_ledger_entries_walletId_transactionId_key UNIQUE ("walletId", "transactionId"),
    CONSTRAINT wallet_ledger_entries_direction_valid CHECK ("direction" IN ('DEBIT', 'CREDIT')),
    CONSTRAINT wallet_ledger_entries_amount_positive CHECK ("amount" > 0),
    CONSTRAINT wallet_ledger_entries_balanceBefore_nonnegative CHECK ("balanceBefore" >= 0),
    CONSTRAINT wallet_ledger_entries_balanceAfter_nonnegative CHECK ("balanceAfter" >= 0),
    CONSTRAINT wallet_ledger_entries_arithmetic CHECK (
        ("direction" = 'CREDIT' AND "balanceAfter" = "balanceBefore" + "amount")
        OR ("direction" = 'DEBIT' AND "balanceAfter" = "balanceBefore" - "amount")
    )
);

CREATE INDEX wallet_ledger_entries_wallet_created_idx ON wallet_ledger_entries ("walletId", "createdAt", "id");

CREATE TABLE reversal_claims (
    "id" UUID PRIMARY KEY,
    "referenceTransactionId" UUID NOT NULL REFERENCES wager_transactions ("id"),
    "reversalTransactionId" UUID NOT NULL REFERENCES wager_transactions ("id"),
    "createdAt" TIMESTAMPTZ NOT NULL,
    CONSTRAINT reversal_claims_referenceTransactionId_key UNIQUE ("referenceTransactionId"),
    CONSTRAINT reversal_claims_reversalTransactionId_key UNIQUE ("reversalTransactionId"),
    CONSTRAINT reversal_claims_distinct CHECK ("referenceTransactionId" <> "reversalTransactionId")
);

CREATE TABLE outbox_events (
    "eventId" TEXT PRIMARY KEY,
    "eventType" TEXT NOT NULL,
    "aggregateId" TEXT NOT NULL,
    "correlationId" TEXT NOT NULL,
    "causationId" TEXT,
    "occurredAt" TIMESTAMPTZ NOT NULL,
    "version" INTEGER NOT NULL,
    "payload" BYTEA NOT NULL,
    "createdAt" TIMESTAMPTZ NOT NULL,
    "publishedAt" TIMESTAMPTZ,
    "nextAttemptAt" TIMESTAMPTZ NOT NULL,
    "attempts" INTEGER NOT NULL DEFAULT 0,
    "claimedUntil" TIMESTAMPTZ,
    CONSTRAINT outbox_events_version_one CHECK ("version" = 1),
    CONSTRAINT outbox_events_type_valid CHECK ("eventType" IN ('WagerTransactionProcessed', 'WagerTransactionRejected', 'WalletBalanceChanged', 'WagerTransactionPendingReference'))
);

CREATE INDEX outbox_events_due_idx ON outbox_events ("nextAttemptAt") WHERE "publishedAt" IS NULL;

GRANT USAGE ON SCHEMA public TO wager_app;
GRANT ALL ON TABLE wallets, wager_transactions, reversal_claims, outbox_events TO wager_app;
GRANT INSERT, SELECT ON wallet_ledger_entries TO wager_app;
