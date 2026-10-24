# Backup and restore

Pocket Audiobooks supports two backup formats. Both snapshot SQLite consistently;
neither copies a live `.db` file or overwrites an existing destination.

- **Application bundle** (`.tar.gz`): database, `secret.key`, optional Core token/OIDC
  credentials, and all managed artwork referenced by that database, including
  recoverable artwork in Trash. Each file has a SHA-256 checksum in a manifest.
- **Database snapshot** (`.db`): database only, retained for existing backup jobs.

In native mode the database owns the catalog, accounts, progress, bookmarks,
reading positions, listening history, lists, shares, settings and job journals.
`secret.key` decrypts saved provider/account credentials and signs share grants.
It is required in a bundle. Application bundles contain credentials: keep them
private and include them in encrypted off-machine backups.

## Make a backup

Admins can use Settings → Backup & data → **Download application backup** while
this server is running. Native Trash moves pause briefly while its database and
managed artwork are captured. A pending/failed Trash operation must be completed
or recovered first; a database-only snapshot remains available for diagnosis.

```sh
curl -H "Authorization: Bearer pab_…" \
  'https://audio.example.com/api/v1/backup?format=bundle' -o audiobooks.tar.gz
```

For an offline bundle, stop the app and run its binary against that data directory:

```sh
DATA_DIR=/srv/pocket-audiobooks MEDIA_BACKEND=native \
  pocket-audiobooks backup --bundle -o /private/audiobooks.tar.gz
```

The server and offline bundle command share an exclusive process lease. The
command refuses to run while that server uses the directory; use the authenticated
web download for an online bundle. Leases release automatically after a crash.
Runtime containers and filesystem moves currently target Linux.

Database-only backups still work online:

```sh
docker exec pocket-audiobooks pocket-audiobooks backup
curl -H "Authorization: Bearer pab_…" \
  https://audio.example.com/api/v1/backup -o audiobooks.db
```

Default destinations are unique, timestamped entries under `DATA_DIR/backups`.
Snapshot and bundle files are private (0600). Never replace a database with a raw
copy of its live main file: committed changes can still be in its WAL.

## Verify and restore a bundle

```sh
pocket-audiobooks verify-backup --from /private/audiobooks.tar.gz
pocket-audiobooks restore --from /private/audiobooks.tar.gz --to /srv/audiobooks-restored
```

`verify-backup` checks archive entries, checksums, the gzip footer, database
integrity/references, supported schema and required artwork. It uses private
scratch storage and never opens external media. Verification needs enough scratch
disk for the extracted bundle. The supported application-bundle limit is 64 GiB
expanded data and 100,000 files; large audio libraries are separate backups.

Restore requires a **new, nonexistent destination**. It validates into a private
staging directory and publishes the complete directory atomically. It refuses
path traversal, links, duplicate entries, missing credentials, unsupported newer
schemas and modified contents. A failed restore never overwrites an installation.

The managed-art root is rebased to the new data directory. Restored managed Trash
files get verified file identities so restoring them still works on the new disk.
The input bundle manifest is retained as `restore-manifest.json`; its database hash
describes the input before this path rebasing. Existing app tokens, sealed secrets
and progress are preserved. This is a recovery copy, not a second server to run
concurrently against the same media and Core registration.

Before starting the restored app, stop the previous instance, provide its original
environment/compose configuration and mount the external paths listed below. The
manifest reports the selected backend; set `MEDIA_BACKEND` accordingly. Restore
does not change DNS, start jobs, contact Core or copy `.env` secrets automatically.

## Back up media and configuration separately

A bundle **does not include** external audio, ebooks, podcast downloads, imported
covers/author images or external `.pocket-trash` directories. Its
`external_media_roots` inventory names every catalog root and current mount path.
Back up those roots, their Trash directories, and deployment configuration with
Pocket Backups or filesystem snapshots. Coordinate media snapshots with the app
stopped or writes paused when moving/deleting files. A database bundle plus an
unrelated media snapshot is not a guaranteed point-in-time restore.

Mount restored media at the recorded paths. External Trash identities remain
unchanged; copying media to another filesystem can cause Trash to refuse a move or
purge until the files are reconciled. It will not silently adopt unrelated files.
Original imported metadata roots must remain available even after Audiobookshelf
is stopped. Rebuildable HLS/reader caches, temporary downloads and old backups are
not included in the application bundle.

In the legacy `audiobookshelf` backend, the upstream server still owns books,
progress and bookmarks. Back up that server and its media as well.

Each person can export/import their Pocket listening history, finishes and Up next
as JSON. That export is not a full library or native account recovery format.
