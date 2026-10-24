-- Retire the former podcast feature without deleting external media or recovery
-- records. Archived libraries cannot be played, scanned or downloaded. The app
-- only exposes book libraries; podcast background workers no longer start.
INSERT INTO catalog_library_state(library_id,revision,archived)
SELECT id,1,1 FROM catalog_libraries WHERE media_type='podcast'
ON CONFLICT(library_id) DO UPDATE SET archived=1,revision=revision+1;
UPDATE catalog_podcast_subscriptions SET auto_download=0;
UPDATE catalog_podcast_jobs SET status='failed',error='Podcast support retired; use the dedicated podcast app' WHERE status IN ('queued','running');
UPDATE catalog_scan_settings SET enabled=0 WHERE library_id IN (SELECT id FROM catalog_libraries WHERE media_type='podcast');
