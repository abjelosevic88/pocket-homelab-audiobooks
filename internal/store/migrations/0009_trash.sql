-- The journal is committed before moving files. Removed entries are hidden
-- immediately; interrupted filesystem work can be resumed without overwrites.
CREATE TABLE catalog_trash (
 id TEXT PRIMARY KEY,
 item_id TEXT NOT NULL REFERENCES catalog_items(id),
 episode_id TEXT NOT NULL DEFAULT '',
 user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
 title TEXT NOT NULL,
 move_files INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL CHECK(state IN ('moving','deleted','restoring','restored','purging','purged','error')),
 operation TEXT NOT NULL DEFAULT 'delete',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 files TEXT NOT NULL CHECK(json_valid(files)),
 error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE catalog_removed_items (
 item_id TEXT PRIMARY KEY REFERENCES catalog_items(id),
 trash_id TEXT NOT NULL REFERENCES catalog_trash(id)
);
CREATE TABLE catalog_removed_files (
 item_id TEXT NOT NULL REFERENCES catalog_items(id),
 file_id TEXT NOT NULL,
 trash_id TEXT NOT NULL REFERENCES catalog_trash(id),
 PRIMARY KEY(item_id,file_id)
);
