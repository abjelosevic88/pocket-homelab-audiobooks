CREATE TABLE catalog_shares (
 id TEXT PRIMARY KEY,
 slug TEXT NOT NULL UNIQUE,
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('item','series','collection')),
 target_id TEXT NOT NULL,
 episode_id TEXT NOT NULL DEFAULT '',
 name TEXT NOT NULL,
 password_hash TEXT NOT NULL DEFAULT '',
 downloadable INTEGER NOT NULL DEFAULT 0,
 expires_at INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX catalog_shares_owner ON catalog_shares(user_id,created_at);
