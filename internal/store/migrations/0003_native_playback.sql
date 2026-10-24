CREATE TABLE catalog_play_heads (
 account_id TEXT NOT NULL REFERENCES catalog_accounts(id),
 item_id TEXT NOT NULL REFERENCES catalog_items(id),
 episode_id TEXT NOT NULL DEFAULT '',
 generation INTEGER NOT NULL,
 PRIMARY KEY(account_id,item_id,episode_id)
);
CREATE TABLE catalog_play_sessions (
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES catalog_accounts(id),
 item_id TEXT NOT NULL REFERENCES catalog_items(id),
 episode_id TEXT NOT NULL DEFAULT '',
 generation INTEGER NOT NULL,
 device TEXT NOT NULL,
 client TEXT NOT NULL,
 started_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 position REAL NOT NULL CHECK(position>=0),
 start_position REAL NOT NULL CHECK(start_position>=0),
 duration REAL NOT NULL CHECK(duration>0),
 listened REAL NOT NULL DEFAULT 0 CHECK(listened>=0),
 playing INTEGER NOT NULL DEFAULT 0,
 closed INTEGER NOT NULL DEFAULT 0,
 last_observed_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX catalog_play_recent ON catalog_play_sessions(account_id,updated_at DESC);
CREATE TABLE catalog_play_reports (
 account_id TEXT NOT NULL REFERENCES catalog_accounts(id),
 id TEXT NOT NULL,
 session_id TEXT NOT NULL REFERENCES catalog_play_sessions(id),
 fingerprint TEXT NOT NULL,
 received_at INTEGER NOT NULL,
 PRIMARY KEY(account_id,id)
);
CREATE TABLE catalog_play_days (
 session_id TEXT NOT NULL REFERENCES catalog_play_sessions(id),
 day TEXT NOT NULL,
 seconds REAL NOT NULL CHECK(seconds>=0),
 PRIMARY KEY(session_id,day)
);
