-- Audible metadata provider (region per provider, match order) and bulk quick-match jobs.
CREATE TABLE catalog_metadata_providers_new (
 id TEXT PRIMARY KEY,
 kind TEXT NOT NULL CHECK(kind IN ('openlibrary','google','custom','audible')),
 name TEXT NOT NULL,
 url TEXT NOT NULL DEFAULT '',
 secret TEXT NOT NULL DEFAULT '',
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 revision INTEGER NOT NULL DEFAULT 0,
 region TEXT NOT NULL DEFAULT '',
 priority INTEGER NOT NULL DEFAULT 100
);
INSERT INTO catalog_metadata_providers_new(id,kind,name,url,secret,enabled,revision,priority)
 SELECT id,kind,name,url,secret,enabled,revision,CASE id WHEN 'openlibrary' THEN 20 WHEN 'google' THEN 30 ELSE 100 END FROM catalog_metadata_providers;
DROP TABLE catalog_metadata_providers;
ALTER TABLE catalog_metadata_providers_new RENAME TO catalog_metadata_providers;
INSERT OR IGNORE INTO catalog_metadata_providers(id,kind,name,enabled,priority) VALUES ('audible','audible','Audible',1,10);

CREATE TABLE catalog_match_jobs (
 id TEXT PRIMARY KEY,
 user_id INTEGER NOT NULL,
 library_id TEXT NOT NULL DEFAULT '',
 provider TEXT NOT NULL DEFAULT '',
 options TEXT NOT NULL DEFAULT '{}',
 items TEXT NOT NULL DEFAULT '[]',
 status TEXT NOT NULL CHECK(status IN ('queued','running','complete','cancelled','failed')),
 total INTEGER NOT NULL DEFAULT 0,
 done INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE INDEX catalog_match_jobs_user ON catalog_match_jobs(user_id, created_at);
CREATE TABLE catalog_match_results (
 job_id TEXT NOT NULL REFERENCES catalog_match_jobs(id) ON DELETE CASCADE,
 position INTEGER NOT NULL,
 item_id TEXT NOT NULL,
 status TEXT NOT NULL,
 result TEXT NOT NULL,
 PRIMARY KEY(job_id, position)
);
