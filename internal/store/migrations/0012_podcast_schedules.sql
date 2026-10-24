ALTER TABLE catalog_podcast_subscriptions ADD COLUMN schedule TEXT NOT NULL DEFAULT '';
ALTER TABLE catalog_podcast_subscriptions ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC';
ALTER TABLE catalog_podcast_subscriptions ADD COLUMN next_check INTEGER NOT NULL DEFAULT 0;
UPDATE catalog_podcast_subscriptions SET next_check=last_check+interval_seconds*1000;
CREATE INDEX catalog_podcast_due ON catalog_podcast_subscriptions(auto_download,next_check);
ALTER TABLE catalog_podcast_subscriptions ADD COLUMN download_watermark INTEGER NOT NULL DEFAULT 0;
UPDATE catalog_podcast_subscriptions SET download_watermark=last_check;
