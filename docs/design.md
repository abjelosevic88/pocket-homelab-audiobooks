# How it works

Pocket Audiobooks is an independent Go server with SQLite and a React web app. The native backend owns its library, listening state and files; it does not require Audiobookshelf. FFprobe reads media information and FFmpeg handles conversion and HLS playback. The accepted scope is a complete everyday audiobook app, not an exact copy of every Audiobookshelf feature.

## Library and files

Libraries index mounted folders. Scanning groups audio files into books, reads chapters and metadata, and preserves manual edits and stable item identifiers. Uploads publish new files without overwriting existing media. Metadata, covers, authors, series, collections and playlists live in the native catalog. Downloads serve original files or a generated archive. Media tools can create a chaptered M4B while retaining originals.

File access is confined to configured roots. Library permissions apply to browsing, downloads and playback. Personal progress, bookmarks, history and queues belong to the signed-in person. Administrative access does not grant another person's listening history.

## Playback

Both the web player and compatible native clients open durable local playback sessions. Direct playback supports byte ranges and multiple tracks on one book timeline. When necessary, local FFmpeg generates an HLS stream. The server validates ownership before serving sessions or accepting progress.

The web player saves progress every 15 seconds and on pause, with a durable retry queue for interrupted connections. Playback controls include speed, skipping, chapters, sleep timer, bookmarks and Up next. Offline client reports reconcile with saved progress. Session reports supply listening history, daily totals, streaks and finished books.

The compatibility API retains the paths and JSON shapes used by Pocket Homelab's audiobook client. Pocket Core supplies its device token; no separate audiobook password is needed. Compatibility with arbitrary third-party clients is not a claim of complete Audiobookshelf parity.

## Pocket integrations

Pocket Core provides sign-in, device authentication and scoped app links. Pocket Day reads listening summaries, Pocket Bookshelf reads started and finished books, and Homepage reads dashboard statistics. These integrations use the native data directly.

## Migration and recovery

The offline importer reads a consistent Audiobookshelf SQLite snapshot, preserves library/item IDs and maps selected source accounts to Pocket users. Excluded accounts and their personal state are not imported. Original media is retained in place. Source credentials are not needed by the native backend.

Application backup bundles contain the database, application secrets and managed artwork. External library folders require their own backup. Restore validates the bundle into a new directory. The optional legacy backend remains an explicit rollback aid; native is the default. See [backup](backup.md) and [migration](standalone-migration.md).
