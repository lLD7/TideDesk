-- NULL sums preserve missing data. pcr_ready distinguishes an unprocessed
-- historical snapshot from a processed snapshot with no usable observations.
ALTER TABLE snapshot_iv_summary ADD COLUMN call_volume INTEGER;
ALTER TABLE snapshot_iv_summary ADD COLUMN put_volume INTEGER;
ALTER TABLE snapshot_iv_summary ADD COLUMN call_oi INTEGER;
ALTER TABLE snapshot_iv_summary ADD COLUMN put_oi INTEGER;
ALTER TABLE snapshot_iv_summary ADD COLUMN pcr_ready INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_snapshot_pcr_pending ON snapshot_iv_summary(ticker,source,collected_at) WHERE pcr_ready=0;
ALTER TABLE snapshot_iv_summary ADD COLUMN ribbon_json TEXT;
ALTER TABLE snapshot_iv_summary ADD COLUMN ribbon_model TEXT;
