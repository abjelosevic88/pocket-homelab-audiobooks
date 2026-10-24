CREATE TABLE catalog_scan_runs (
 id TEXT PRIMARY KEY,
 library_id TEXT NOT NULL REFERENCES catalog_libraries(id),
 started_at INTEGER NOT NULL,
 finished_at INTEGER,
 status TEXT NOT NULL CHECK(status IN ('running','complete','failed','cancelled')),
 report TEXT NOT NULL CHECK(json_valid(report))
);
CREATE INDEX catalog_scan_runs_recent ON catalog_scan_runs(library_id,started_at DESC);
CREATE TABLE catalog_file_observations (
 item_id TEXT NOT NULL,
 file_id TEXT NOT NULL,
 size INTEGER NOT NULL,
 modified_ns INTEGER NOT NULL,
 identity TEXT NOT NULL,
 PRIMARY KEY(item_id,file_id),
 FOREIGN KEY(item_id,file_id) REFERENCES catalog_files(item_id,id) ON DELETE CASCADE
);
CREATE TABLE catalog_scan_settings (
 library_id TEXT PRIMARY KEY REFERENCES catalog_libraries(id),
 interval_seconds INTEGER NOT NULL DEFAULT 600 CHECK(interval_seconds>=0),
 enabled INTEGER NOT NULL DEFAULT 0,
 last_started INTEGER NOT NULL DEFAULT 0,
 actor_id INTEGER NOT NULL REFERENCES users(id)
);

CREATE UNIQUE INDEX catalog_scan_one_running ON catalog_scan_runs(library_id) WHERE status='running';
CREATE UNIQUE INDEX catalog_item_location ON catalog_items(library_id,json_extract(document,'$.item.path'))
 WHERE json_extract(document,'$.item.path') IS NOT NULL;
