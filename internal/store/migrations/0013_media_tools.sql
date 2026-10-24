CREATE TABLE catalog_tool_jobs (
 id TEXT PRIMARY KEY,
 user_id INTEGER NOT NULL REFERENCES users(id),
 item_id TEXT NOT NULL REFERENCES catalog_items(id),
 kind TEXT NOT NULL CHECK(kind IN ('m4b','embed')),
 source_revision INTEGER NOT NULL,
 source TEXT NOT NULL CHECK(json_valid(source)),
 options TEXT NOT NULL CHECK(json_valid(options)),
 status TEXT NOT NULL CHECK(status IN ('queued','running','complete','failed','cancelled','applying','applied')),
 progress REAL NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 error TEXT NOT NULL DEFAULT '',
 output_sha256 TEXT NOT NULL DEFAULT '',
 output_size INTEGER NOT NULL DEFAULT 0,
 output_duration REAL NOT NULL DEFAULT 0,
 apply_root TEXT NOT NULL DEFAULT '',
 apply_path TEXT NOT NULL DEFAULT '',
 apply_file_id TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX catalog_tool_active ON catalog_tool_jobs(item_id) WHERE status IN ('queued','running','applying');
CREATE INDEX catalog_tool_owner ON catalog_tool_jobs(user_id,created_at DESC);
