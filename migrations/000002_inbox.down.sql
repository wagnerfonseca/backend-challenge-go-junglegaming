-- Reverse of the durable inbox.

drop index if exists inbox_deliveries_completed_idx;
drop table if exists inbox_deliveries;
