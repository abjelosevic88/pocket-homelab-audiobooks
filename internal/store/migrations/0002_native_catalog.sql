-- Independent catalog. Source identifiers remain stable across the migration.
-- This schema does not change the current upstream-backed player.
CREATE TABLE catalog_imports (
  id TEXT PRIMARY KEY,
  source_fingerprint TEXT NOT NULL,
  imported_at TEXT NOT NULL,
  report TEXT NOT NULL CHECK(json_valid(report))
);
CREATE TABLE catalog_roots (
  id TEXT PRIMARY KEY,
  source_path TEXT NOT NULL UNIQUE,
  local_path TEXT NOT NULL
);
CREATE TABLE catalog_libraries (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  media_type TEXT NOT NULL CHECK(media_type IN ('book', 'podcast')),
  display_order INTEGER NOT NULL,
  document TEXT NOT NULL CHECK(json_valid(document))
);
CREATE TABLE catalog_accounts (
  id TEXT PRIMARY KEY,
  pocket_user_id INTEGER UNIQUE REFERENCES users(id) ON DELETE SET NULL,
  username TEXT NOT NULL,
  active INTEGER NOT NULL CHECK(active IN (0,1)),
  locked INTEGER NOT NULL CHECK(locked IN (0,1)),
  permissions TEXT NOT NULL CHECK(json_valid(permissions))
);
CREATE TABLE catalog_items (
  id TEXT PRIMARY KEY,
  library_id TEXT NOT NULL REFERENCES catalog_libraries(id),
  media_id TEXT NOT NULL UNIQUE,
  media_type TEXT NOT NULL CHECK(media_type IN ('book', 'podcast')),
  title TEXT NOT NULL,
  author TEXT NOT NULL,
  duration REAL NOT NULL CHECK(duration >= 0),
  explicit INTEGER NOT NULL CHECK(explicit IN (0,1)),
  missing INTEGER NOT NULL CHECK(missing IN (0,1)),
  added_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  tags TEXT NOT NULL CHECK(json_valid(tags)),
  document TEXT NOT NULL CHECK(json_valid(document))
);
CREATE INDEX catalog_items_library ON catalog_items(library_id, title, id);
CREATE TABLE catalog_files (
  item_id TEXT NOT NULL REFERENCES catalog_items(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  role TEXT NOT NULL CHECK(role IN ('audio','ebook','cover','supplement')),
  root_id TEXT NOT NULL REFERENCES catalog_roots(id),
  relative_path TEXT NOT NULL,
  name TEXT NOT NULL,
  mime TEXT NOT NULL,
  size INTEGER NOT NULL CHECK(size >= 0),
  duration REAL NOT NULL CHECK(duration >= 0),
  track_index INTEGER NOT NULL CHECK(track_index >= 0),
  excluded INTEGER NOT NULL CHECK(excluded IN (0,1)),
  document TEXT NOT NULL CHECK(json_valid(document)),
  PRIMARY KEY(item_id, id)
);
-- Source entities remain lossless JSON alongside indexed native relationships.
-- This includes episodes, author/series metadata, collections and playlists.
CREATE TABLE catalog_entities (
  kind TEXT NOT NULL,
  id TEXT NOT NULL,
  library_id TEXT REFERENCES catalog_libraries(id),
  document TEXT NOT NULL CHECK(json_valid(document)),
  PRIMARY KEY(kind,id)
);
CREATE TABLE catalog_progress (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES catalog_accounts(id),
  item_id TEXT NOT NULL REFERENCES catalog_items(id),
  episode_id TEXT NOT NULL,
  position REAL NOT NULL CHECK(position >= 0),
  finished INTEGER NOT NULL CHECK(finished IN (0,1)),
  updated_at INTEGER NOT NULL,
  document TEXT NOT NULL CHECK(json_valid(document)),
  UNIQUE(account_id,item_id,episode_id)
);
CREATE TABLE catalog_history (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES catalog_accounts(id),
  item_id TEXT,
  document TEXT NOT NULL CHECK(json_valid(document))
);
CREATE TABLE catalog_bookmarks (
  account_id TEXT NOT NULL REFERENCES catalog_accounts(id),
  item_id TEXT NOT NULL REFERENCES catalog_items(id),
  position REAL NOT NULL CHECK(position >= 0),
  document TEXT NOT NULL CHECK(json_valid(document)),
  PRIMARY KEY(account_id,item_id,position)
);
