CREATE TABLE catalog_podcast_subscriptions (
 item_id TEXT PRIMARY KEY REFERENCES catalog_items(id),
 feed_url TEXT NOT NULL,
 actor_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
 auto_download INTEGER NOT NULL DEFAULT 0,
 interval_seconds INTEGER NOT NULL DEFAULT 3600 CHECK(interval_seconds BETWEEN 300 AND 604800),
 keep_latest INTEGER NOT NULL DEFAULT 0 CHECK(keep_latest>=0),
 max_new INTEGER NOT NULL DEFAULT 3 CHECK(max_new BETWEEN 1 AND 100),
 last_check INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE catalog_podcast_feed_episodes (
 item_id TEXT NOT NULL REFERENCES catalog_items(id),
 episode_id TEXT NOT NULL,
 enclosure_url TEXT NOT NULL,
 published_at INTEGER NOT NULL DEFAULT 0,
 document TEXT NOT NULL CHECK(json_valid(document)),
 PRIMARY KEY(item_id,episode_id)
);
CREATE TABLE catalog_podcast_jobs (
 id TEXT PRIMARY KEY,
 item_id TEXT NOT NULL REFERENCES catalog_items(id),
 episode_id TEXT NOT NULL,
 actor_id INTEGER NOT NULL REFERENCES users(id),
 status TEXT NOT NULL CHECK(status IN ('queued','running','complete','failed','cancelled')),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 error TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX catalog_podcast_active_job ON catalog_podcast_jobs(item_id,episode_id) WHERE status IN ('queued','running');
