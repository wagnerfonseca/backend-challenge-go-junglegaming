-- Reverse of the initial schema.

drop index if exists outbox_events_due_idx;
drop table if exists outbox_events;
drop table if exists reversal_claims;
drop index if exists wallet_ledger_entries_wallet_created_idx;
drop table if exists wallet_ledger_entries;
drop index if exists wager_transactions_due_reference_idx;
drop index if exists wager_transactions_reference_idx;
drop index if exists wager_transactions_opening_walletId_idx;
drop index if exists wager_transactions_provider_externalTransactionId_idx;
drop index if exists wager_transactions_provider_key_idx;
drop table if exists wager_transactions;
drop table if exists wallets;
