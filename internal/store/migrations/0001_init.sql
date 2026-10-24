CREATE TABLE users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'member',
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT
);

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  token_hash   TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL DEFAULT '',
  last_used_at TEXT,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Box-wide settings (title, setup flag); everything else is personal.
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE user_settings (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, key)
);

CREATE TABLE cache (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL
);


-- How a person reaches Audiobookshelf: an API key (kind key: their own,
-- made in Audiobookshelf under Settings → API Keys, or the owner's from the
-- environment), a password sign-in of which only the refresh token is kept
-- (kind direct, sealed with DATA_DIR/secret.key; Audiobookshelf rotates it),
-- or the login Pocket Core holds for them (kind core: nothing stored here).
-- No row = Core if the box has one.
CREATE TABLE connections (
  user_id        INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  kind           TEXT NOT NULL DEFAULT 'key',     -- key | direct | core | off
  server_url     TEXT NOT NULL DEFAULT '',        -- '' = AUDIOBOOKSHELF_URL
  username       TEXT NOT NULL DEFAULT '',
  abs_user_id    TEXT NOT NULL DEFAULT '',
  secret         TEXT NOT NULL DEFAULT '',        -- sealed API key / refresh token
  access         TEXT NOT NULL DEFAULT '',        -- sealed access token of a password sign-in
  server_version TEXT NOT NULL DEFAULT '',
  updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Listening history owned by Pocket Audiobooks: a copy of each person's
-- Audiobookshelf listening sessions (its own player's and every other app's),
-- so history and stats survive a book leaving the library. One row per
-- Audiobookshelf session; a session that grows is updated in place.
CREATE TABLE listens (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  abs_id      TEXT NOT NULL,                    -- the session id in Audiobookshelf ('' for imports)
  item_id     TEXT NOT NULL,
  library_id  TEXT NOT NULL DEFAULT '',
  title       TEXT NOT NULL DEFAULT '',
  author      TEXT NOT NULL DEFAULT '',
  narrator    TEXT NOT NULL DEFAULT '',
  series      TEXT NOT NULL DEFAULT '',
  genres      TEXT NOT NULL DEFAULT '',         -- '|'-joined with leading/trailing '|'
  duration    INTEGER NOT NULL DEFAULT 0,       -- the book's length, seconds
  day         TEXT NOT NULL,                    -- local date the session started
  started_at  TEXT NOT NULL,                    -- UTC
  updated_at  TEXT NOT NULL,                    -- UTC, the last report
  seconds     INTEGER NOT NULL DEFAULT 0,       -- seconds actually listened
  start_pos   INTEGER NOT NULL DEFAULT 0,
  end_pos     INTEGER NOT NULL DEFAULT 0,
  device      TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL DEFAULT 'audiobookshelf', -- pocket | audiobookshelf | import
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX listens_abs ON listens(user_id, abs_id) WHERE abs_id <> '';
CREATE INDEX listens_time ON listens(user_id, started_at);
CREATE INDEX listens_day ON listens(user_id, day);
CREATE INDEX listens_item ON listens(user_id, item_id);

-- Books a person finished (from Audiobookshelf's finished marks), kept so a
-- reset or a removed book doesn't take the year's count with it.
CREATE TABLE finishes (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_id     TEXT NOT NULL,
  title       TEXT NOT NULL DEFAULT '',
  author      TEXT NOT NULL DEFAULT '',
  duration    INTEGER NOT NULL DEFAULT 0,
  finished_at TEXT NOT NULL,                    -- UTC
  day         TEXT NOT NULL,
  UNIQUE (user_id, item_id, day)
);
CREATE INDEX finishes_time ON finishes(user_id, finished_at);

CREATE TABLE listen_sync (
  user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  server_key TEXT NOT NULL DEFAULT '',  -- server + Audiobookshelf user the history came from
  watermark  INTEGER NOT NULL DEFAULT 0, -- newest session updatedAt (ms) already read
  backfilled INTEGER NOT NULL DEFAULT 0,
  synced_at  TEXT NOT NULL DEFAULT '',
  error      TEXT NOT NULL DEFAULT ''
);

-- Books a person wants to listen to next, in their order.
CREATE TABLE up_next (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_id   TEXT NOT NULL,
  title     TEXT NOT NULL DEFAULT '',
  author    TEXT NOT NULL DEFAULT '',
  position  INTEGER NOT NULL DEFAULT 0,
  added_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (user_id, item_id)
);
