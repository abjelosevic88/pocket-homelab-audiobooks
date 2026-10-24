# Standalone Pocket Audiobooks migration

Status: stage 1 implemented and rehearsed; the live application still uses
Audiobookshelf. Full feature parity and cutover are **not complete**.

The owner selected an independent Go implementation, replacing every feature in
stages. Keep the existing Pocket stack: Go, SQLite, React/TypeScript, one app
container and Pocket Core authentication. No forked server or separate Node
backend is part of the replacement. Python is used only for operator preflight
scripts, not by the application at runtime.

## Stages and current evidence

1. **Native catalog and migration foundation (implemented).** New SQLite tables,
   transactional import from a read-only snapshot, explicit account mapping,
   stable source IDs, permission-filtered browse/progress reads, and local file
   serving with range/HEAD requests. This stage is additive and does not switch
   the existing web/native players.
2. **Native playback and progress.** Sessions, multi-file timelines, bookmarks,
   resume/finish, durable history, retry/offline conflict rules and live handoff.
   Connect the web/native players and Pocket integration contracts to this engine.
3. **Library ingestion and editing.** Scanner/watcher, uploads, file moves,
   metadata/chapters/covers, matching providers, authors/series and bulk actions.
4. **Remaining feature parity.** Collections/playlists, transcoding and tools,
   ebooks/ereaders, podcast lifecycle, RSS/sharing, account and operations tooling,
   and third-party client protocols. The matrix below is the acceptance scope.
5. **Final migration and cutover.** Reconcile all data/settings and client behavior,
   preserve rollback state, switch routing, and verify with the old server stopped.

The first rehearsal imported a private snapshot into a separate staging database:
1,506 books, 11 podcasts, 77 episodes, five libraries, 151 authors, 226 series,
11 progress records and 92 listening sessions. The native catalog holds all three
source accounts; only the one identity proven by Pocket's existing sync mapping
was bound to a Pocket account. Two accounts remain unmapped and cannot access the
catalog until explicitly linked. No source passwords, tokens or email addresses
were loaded into the catalog accounts table.

There are 15,656 indexed file records, including cover/episode records beyond the
initial libraryFiles inventory. Five unavailable records belong to two podcast
items and are retained with their original metadata. These are pre-existing
source issues, not silently dropped data. Record counts are not unique-file counts.
The snapshot, metadata archive and reports remain in a private operator directory
outside Git. Source media was opened read-only and never copied or modified.

Validation: synthetic tests cover atomic rollback, dry-run/reimport behavior,
permissions (including admin, explicit, tag, locked and unmapped accounts),
per-person progress, missing files, path/symlink escapes, auth and HTTP ranges.
The full snapshot rehearsal verifies all five libraries/1,517 visible items and
exact leading/trailing byte ranges for four audio MIME types while upstream
network transports are blocked. This is file-serving evidence, not yet proof of
playback, client compatibility or transcoding.

## Preserve at migration

- Library, item, author, series, folder and file identifiers; order and sequence.
- Original media, ebooks and companion files, tags, custom metadata, covers,
  author images, chapters, track order, durations and offsets.
- All user accounts, account status, library/tag permissions, progress, finished
  timestamps, bookmarks, preferences, history and device associations.
- Collections, playlists, public feeds, media shares and their access controls.
- Podcast subscriptions, downloaded episodes, download/retention settings and
  episode state. Existing Pocket Podcasts does not imply this data may be dropped.
- Metadata providers, notifications, email/ereader configuration, scheduled work,
  scanner settings and backups. Credentials must remain private.
- Pocket Core identity and device authentication, app passwords, Homepage,
  Pocket Day and Pocket Bookshelf contracts and stable book references.

Existing sessions and API keys require an explicit compatibility strategy; do not
copy credential bytes into an incompatible authentication implementation and
claim that existing clients will keep working.

## Feature acceptance matrix

Every row must have implemented behavior, meaningful tests and migration evidence
before claiming full parity. Existing wrappers and upstream proxies do not count
as a standalone implementation.

| Area | Required behavior | Standalone verification |
| --- | --- | --- |
| Libraries | Multiple libraries/folders; ordering; book and podcast media; access restrictions | Imported browse/access verified; management pending |
| Scanning | Incremental scan, watcher, manual/rescan, missing files, regrouping, file moves | Pending |
| Book ingestion | Single/multiple audio files, ebooks, tags, sidecars, chapters, covers, track order | Pending |
| Library management | Uploads, metadata/chapter editing, bulk operations, download, safe deletion | Pending |
| Metadata | Built-in/custom providers, matching, covers, authors, series, genres, tags, narrators | Pending |
| Browse | Search, filters, sorts, pagination, shelves, series and authors | Pending |
| Playback | Direct/range streaming, multi-file timeline, transcoding/HLS, seeking | Local range/HEAD serving verified; playback engine pending |
| Personal state | Progress, bookmarks, completion, hidden items, history, statistics | Import and progress isolation verified; native writes pending |
| Device sync | Sessions, handoff, retries, offline reports, conflicts, live updates | Pending |
| Clients | Native Pocket app API, third-party login, refresh, token streams, Socket.IO | Pending |
| Organisation | Collections, personal playlists, bulk add/remove/reorder | Pending |
| Ebooks | Serving/reading contract, reading progress, companion books, sending to ereaders | Pending |
| Podcasts | Feeds/OPML, discovery, episode checks/downloads, scheduling and retention | Pending |
| Sharing | RSS for books/series/collections, media shares, passwords/expiry/download permissions | Pending |
| Accounts | Local/SSO authentication, per-user isolation, permissions, API-key lifecycle | Explicit mappings and restrictions verified; full migration pending |
| Tools | Metadata embedding, M4B conversion, jobs, cancellation, status | Pending |
| Operations | Backups/restore, logging, cache management, settings and notifications | Pending |
| Pocket integrations | Core pairing, Day, Bookshelf, Homepage, notifications and backup plans | Existing interfaces; standalone verification pending |

Use the installed server's router/controllers and native app service client as
contract evidence. Do not assume a gateway allowlist describes the entire server.

## Preflight tooling

`python3 scripts/migration/inventory.py --database /path/to/absdatabase.sqlite`
opens SQLite in read-only mode and prints schema/data counts without credentials.
For a filesystem audit, supply `--mounts mounts.json` containing a JSON object
mapping container roots to host roots. Paths escaping a mount, including symlink
escapes, are not accepted as mapped files.

Add `--snapshot-dir /private/new-directory` and optionally
`--pocket-database /path/to/pocket-audiobooks.db` to create private consistent
SQLite snapshots. The destination must not exist. Each database is individually
consistent; this is not a simultaneous snapshot of both services. Take final
snapshots during a controlled write pause before cutover. Snapshot files contain
sensitive authentication data and must never be checked into Git.

Run the tool tests with:

```sh
python3 -m unittest discover -s scripts/migration -v
```

## Native import command

Build the existing app normally. Start with a **separate copy** of its SQLite
snapshot so it contains the intended Pocket user IDs. Create a private `users.json`
map of source account IDs to existing Pocket user IDs, for example
`{"source-account-id": 1}`. Omitted accounts are preserved but unbound; mappings
are never guessed from usernames, email addresses or admin roles. Create a
`mounts.json` map, for example `{"/audiobooks": "/srv/books", "/metadata":
"/srv/metadata"}`. Every indexed path must have a mapping. The folders must exist;
individual missing files are reported and preserved.

```sh
DATA_DIR=/private/rehearsal DB_PATH=/private/rehearsal/pocket-audiobooks.db \
  ./pocket-audiobooks import-audiobookshelf \
  --source /private/snapshot/audiobookshelf.sqlite \
  --mounts /private/mounts.json --users /private/users.json
```

Without `--apply`, the importer validates all rows and database constraints in a
transaction, then rolls back the catalog data. Opening a target database still
applies the application's schema migrations. With `--apply`, catalog data commits
atomically. An existing catalog is refused, including repeat imports: there is
no automatic merge or overwrite. A final delta/reconciliation workflow is still
required before live cutover.

The importer preserves library/item metadata, files, author/series relationships,
podcast episodes, collections/playlists, accounts/permissions, progress, bookmarks
and history. Importing entity data does not implement its management features.
Devices, shares/feeds, notifications, settings, custom providers and authentication
material remain in the source snapshot for later migration stages; this command
does not claim to migrate those into functioning native services.

A gated test copies a populated staging database and runs native HTTP checks with
no background jobs or upstream calls:

```sh
POCKET_NATIVE_REHEARSAL_DB=/private/rehearsal/pocket-audiobooks.db \
  go test ./internal/server -run TestNativeCatalogSnapshotRehearsal -v -count=1
```

The source staging database opens read-only and media is read-only. The temporary
test token exists only in the test's disposable database copy. Do not point this
at a source Audiobookshelf database; it expects the **imported Pocket catalog**.

## Deployment gates

1. Complete the feature implementation and contract tests against isolated data.
2. Rehearse migration using snapshots and original media mounted read-only. Keep
   scanner/download/notification jobs disabled while rehearsing.
3. Reconcile row counts, identifiers, permissions, progress, history, media paths
   and existing missing-file records. Check actual bytes for streamed ranges.
4. Test multi-file playback, chapter jumps, transcoding, interrupted sessions,
   restart recovery, offline sync and two-user access isolation.
5. Verify the real native clients and required third-party clients, plus Core,
   Day, Bookshelf and Homepage. Retain test evidence and state any unavailable
   client validation explicitly.
6. Pause writes, snapshot again, import the final delta, and switch service routing.
   Keep original databases/configuration and media intact for rollback.
7. Demonstrate the replacement with the old Audiobookshelf service stopped and
   inaccessible. A bundled proxy to it is not a completed independent migration.
8. Retire the old service only after acceptance; retain recoverable backups.

Rollback after new listening requires reconciling that new state, not simply
restoring an old snapshot and discarding it.
