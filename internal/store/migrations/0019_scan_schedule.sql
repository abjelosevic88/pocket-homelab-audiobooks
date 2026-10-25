-- Automatic scans may be limited to a daily time window (minutes after local
-- midnight; end before start wraps past midnight). NULL means any time.
ALTER TABLE catalog_scan_settings ADD COLUMN window_start INTEGER CHECK(window_start IS NULL OR window_start BETWEEN 0 AND 1439);
ALTER TABLE catalog_scan_settings ADD COLUMN window_end INTEGER CHECK(window_end IS NULL OR window_end BETWEEN 0 AND 1439);
-- Scan runs remember whether the schedule or a person started them.
ALTER TABLE catalog_scan_runs ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual' CHECK(origin IN ('manual','auto'));
