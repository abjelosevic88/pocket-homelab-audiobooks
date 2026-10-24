CREATE TABLE catalog_metadata_providers (
 id TEXT PRIMARY KEY,
 kind TEXT NOT NULL CHECK(kind IN ('openlibrary','google','custom')),
 name TEXT NOT NULL,
 url TEXT NOT NULL DEFAULT '',
 secret TEXT NOT NULL DEFAULT '',
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 revision INTEGER NOT NULL DEFAULT 0
);
INSERT INTO catalog_metadata_providers(id,kind,name,enabled) VALUES
 ('openlibrary','openlibrary','Open Library',1),('google','google','Google Books',0);
