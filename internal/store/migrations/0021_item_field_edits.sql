-- Fields a person (book editor, bulk editor, match) set on purpose. Library
-- tidy-up never rewrites them. Rows outlive trashed items harmlessly.
CREATE TABLE catalog_item_edits (
 item_id TEXT NOT NULL,
 field TEXT NOT NULL,
 edited_at INTEGER NOT NULL,
 PRIMARY KEY(item_id, field)
);
