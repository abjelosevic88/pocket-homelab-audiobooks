CREATE TABLE catalog_library_state (
 library_id TEXT PRIMARY KEY REFERENCES catalog_libraries(id),
 revision INTEGER NOT NULL DEFAULT 0,
 archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1))
);
