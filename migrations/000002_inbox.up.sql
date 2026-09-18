-- Durable SQS inbox: one record per (consumerName, messageId) with the
-- received payload digest and the optional completion time. Rows are never
-- deleted: replay safety outlives the broker retention window.

CREATE TABLE inbox_deliveries (
    "id" UUID PRIMARY KEY,
    "consumerName" TEXT NOT NULL,
    "messageId" TEXT NOT NULL,
    "digest" TEXT NOT NULL,
    "receivedAt" TIMESTAMPTZ NOT NULL,
    "completedAt" TIMESTAMPTZ,
    CONSTRAINT inbox_deliveries_consumerName_messageId_key UNIQUE ("consumerName", "messageId"),
    CONSTRAINT inbox_deliveries_digest_hex CHECK ("digest" ~ '^[0-9a-f]{64}$'),
    CONSTRAINT inbox_deliveries_messageId_nonempty CHECK (length("messageId") > 0)
);

CREATE INDEX inbox_deliveries_completed_idx ON inbox_deliveries ("consumerName", "completedAt");

GRANT ALL ON TABLE inbox_deliveries TO wager_app;
