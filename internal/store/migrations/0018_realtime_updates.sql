-- Live updates find changed catalog items by their last change time.
CREATE INDEX IF NOT EXISTS catalog_items_updated ON catalog_items(updated_at, id);
