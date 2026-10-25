-- Review queue for uncertain quick-match results: one row per reviewed result.
-- A result without a row is still pending.
CREATE TABLE catalog_match_reviews (
 job_id TEXT NOT NULL,
 position INTEGER NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('accepted','skipped')),
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(job_id, position),
 FOREIGN KEY(job_id, position) REFERENCES catalog_match_results(job_id, position) ON DELETE CASCADE
);
