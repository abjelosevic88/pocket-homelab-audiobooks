CREATE TABLE catalog_podcast_episode_edits (
 item_id TEXT NOT NULL REFERENCES catalog_items(id),
 episode_id TEXT NOT NULL,
 document TEXT NOT NULL,
 PRIMARY KEY(item_id,episode_id)
);
