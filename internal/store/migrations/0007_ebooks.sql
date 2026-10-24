-- Companion ebooks keep separate positions. The primary ebook additionally
-- shares ebookLocation/ebookProgress with the existing client progress contract.
CREATE TABLE catalog_ebook_positions (
 account_id TEXT NOT NULL REFERENCES catalog_accounts(id),
 item_id TEXT NOT NULL REFERENCES catalog_items(id),
 file_id TEXT NOT NULL,
 location TEXT NOT NULL,
 progress REAL NOT NULL CHECK(progress >= 0 AND progress <= 1),
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(account_id,item_id,file_id)
);
