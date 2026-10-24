# Standalone Pocket Audiobooks migration

Status: native playback and core library workflows implemented and rehearsed; the live application still uses
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
2. **Native playback and progress (implemented behind `MEDIA_BACKEND=native`).**
   Durable sessions, multi-file timelines, bookmarks, resume/finish, history,
   stable report receipts, device handoff, web retry outbox, local FFmpeg/HLS,
   and Socket.IO v4 WebSocket/polling. The existing web routes and client API
   dispatch into Go handlers in-process. The compatibility adapter makes no
   upstream requests. Actual phone-app acceptance and the remaining endpoints
   in the matrix are still required before cutover.
3. **Library ingestion and editing (core workflow implemented).** Incremental
   scans, configurable polling for changes, persistent/cancellable scan jobs,
   validated uploads, folder creation, metadata/chapter/cover editing and author/
   series associations are implemented. Same-root folder moves retain IDs.
   JSON/description/narrator sidecars are read for new books, and explicit sidecar
   imports preserve ordinary manual edits. Scans reject concurrent edits. Provider
   matching, legacy metadata.abs, cross-root moves and bulk tooling remain. Recoverable deletion is implemented.
4. **Remaining feature parity.** Collection and personal playlist CRUD, ordering,
   imported associations and streamed original-file ZIP downloads are implemented.
   Further work includes transcoding and tools,
   ebooks/ereaders, podcast lifecycle, RSS/sharing, account and operations tooling,
   and third-party client protocols. The matrix below is the acceptance scope.
5. **Final migration and cutover.** Reconcile all data/settings and client behavior,
   preserve rollback state, switch routing, and verify with the old server stopped.

The first rehearsal imported a private snapshot into a separate staging database:
1,506 books, 11 podcasts, 77 episodes, five libraries, 151 authors, 226 series,
11 progress records and 92 listening sessions. The native catalog holds all three
source accounts; only the one identity proven by Pocket's existing sync mapping
was bound to a Pocket account. The owner subsequently requested that only `root` migrate. The final import will
explicitly exclude `fika` and `raycast`, including their personal state and owned lists;
the rehearsal snapshot still contains the original accounts. No source passwords, tokens or email addresses
were loaded into the catalog accounts table.

The owner-only rehearsal subsequently imported the same snapshot with explicit
account exclusions: one mapped account, nine progress records and 63 sessions.
The exclusion report accounts for two omitted accounts, two progress records and
29 sessions. All 1,517 library items and 15,656 file records remain indexed.
All 248 imported EPUB archives passed read-only preparation for the native reader,
including the three using standard font obfuscation. Generated EPUB and PDF browser
tests verify reading, chapter jumps, per-file positions and saved-position reloads.

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
network transports are blocked. The rehearsal also opens native playback sessions
and saves progress for all four formats, and exercises Home, libraries, stats and
native-client overview routes. Synthetic tests cover web-to-client resume,
duplicate reports, restart durability, empty close requests, permission isolation,
WebSocket/polling authentication and revocation. Generated audio tests decode HLS
segments with FFmpeg, including seeking into a second file. Browser outbox tests
simulate a server commit followed by a lost response and page reload. These are
protocol tests, not a claim of testing every third-party phone app.

## Preserve at migration

- Library, item, author, series, folder and file identifiers; order and sequence.
- Original media, ebooks and companion files, tags, custom metadata, covers,
  author images, chapters, track order, durations and offsets.
- The retained root account (fika/raycast explicitly excluded), account status, library/tag permissions, progress, finished
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
| Scanning | Incremental scan, watcher, manual/rescan, missing files, regrouping, file moves | Incremental/manual scans, polling, missing records and same-root moves verified; regrouping/cross-root moves pending |
| Book ingestion | Single/multiple audio files, ebooks, tags, sidecars, chapters, covers, track order | Audio tags, natural ordering, embedded chapters, covers and JSON/text sidecars verified; legacy metadata.abs and multi-disc grouping pending |
| Library management | Uploads, metadata/chapter editing, bulk operations, download, safe deletion | Validated uploads, metadata/chapters/covers and single-file and streamed book ZIP downloads implemented; recoverable deletion/restore/purge implemented; bulk editing tools pending |
| Metadata | Built-in/custom providers, matching, covers, authors, series, genres, tags, narrators | Open Library, Google Books and custom provider search/preview, detail lookup and cover fetching implemented; Open Library live contract and browser selection verified. Other built-in providers and automated matching pending |
| Browse | Search, filters, sorts, pagination, shelves, series and authors | Pending |
| Playback | Direct/range streaming, multi-file timeline, transcoding/HLS, seeking | Direct/multi-file playback and local seekable FFmpeg/HLS verified |
| Personal state | Progress, bookmarks, completion, hidden items, history, statistics | Native writes, retries, resume/finish and isolation verified |
| Device sync | Sessions, handoff, retries, offline reports, conflicts, live updates | Native engine, web outbox and cumulative offline session uploads verified, including concurrent/reordered retries, imported-session reconciliation and newer-position protection; legacy online delta-only clients need stable report IDs for exact retry accounting |
| Clients | Native Pocket app API, third-party login, refresh, token streams, Socket.IO | Pending |
| Organisation | Collections, personal playlists, bulk add/remove/reorder | Imported associations, CRUD, ordered batch changes and private ownership verified in API/store tests; create/edit verified in the browser |
| Ebooks | Serving/reading contract, reading progress, companion books, sending to ereaders | EPUB/PDF web reading, chapter navigation, text size, EPUB CFI/PDF page resume, separate companion positions, optimistic conflict checks, durable browser saves and original-file client downloads implemented. Browser EPUB/PDF save/reload verified. Ereader delivery and remaining formats/tools pending |
| Podcasts | Feeds/OPML, discovery, episode checks/downloads, scheduling and retention | RSS/Atom subscriptions, OPML import/export, checks, scheduled new-episode downloads, persistent/cancellable jobs, validation, missing-file repair and web/client playback implemented. Browser subscribe/download/play/save/reload verified. Recoverable keep-latest retention, episode/show deletion, Apple Podcasts search and persistent episode metadata/chapter editing implemented. Client feed preview, creation, OPML parsing, checks, search, queue and episode read/edit/delete routes implemented; bulk import/matching and remaining library compatibility routes pending |
| Sharing | RSS for books/series/collections, media shares, passwords/expiry/download permissions | Native RSS and share pages for books, series, collections and podcast episodes; password grants, expiry/revocation, current publisher permissions, per-episode media boundaries, original downloads and HLS fallback implemented. Browser create/play/resume/RSS/revoke and protocol isolation tests pass; source snapshot has no published feeds |
| Accounts | Local/SSO authentication, per-user isolation, permissions, API-key lifecycle | Admin mapping/grants, library/tag/explicit permissions, locking, stale-update rejection and account ownership verified; full migration pending |
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
`{"source-account-id": 1}`. By default, omitted accounts are preserved but unbound. Use
`--exclude-accounts source-id,source-id` only for explicitly discarded accounts;
this also excludes their progress, history, bookmarks and owned lists, records
excluded row counts, and rejects IDs that are unknown or also mapped. Source
snapshots and shared media remain untouched. Mappings
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

## Native playback rehearsal

Set `MEDIA_BACKEND=native` only with a populated staging catalog and correct local
mounts. `FFMPEG_PATH` and `FFPROBE_PATH` default to binaries on PATH; FFmpeg is now
included in the container. HLS segments are generated locally on demand with a
bounded concurrency and deadline. No media files are modified. Source account
access remains enforced even for a Pocket administrator.

Native web reports include `report_id` and `observed_at` and commit their receipt,
position, listening time and finish atomically. A duplicate receipt cannot add
listening time twice. Superseded device sessions can contribute late listening
history but cannot overwrite the new device's position. The browser persists
pending reports per Pocket user until acknowledged. Third-party clients that
only send deltas without stable IDs cannot have exact retry accounting guaranteed.

The backend switch is an implementation/testing option, **not yet a production
cutover instruction**. Several client
endpoints, metadata providers and advanced library tools and the other rows marked pending still require independent services.

```sh
node scripts/test-playback-outbox.mjs
go test -race ./...
```

## Native ingestion and editor evidence

Stage 3 now adds libraries, streamed/validated uploads, incremental scans,
manual full rescans, scheduled polling (one minute to one day), cancellable jobs,
metadata/author/series/chapter editing with stale-edit rejection, and local cover
uploads. Go handles all orchestration and SQLite state; FFprobe/FFmpeg are local
media tools in the same container. React uses the existing Pocket settings,
forms, modal and book-page components.

Scans preserve imported IDs and edits, never delete original files, and retain
missing tracks so their disappearance cannot silently shift a book's timeline.
Directory moves within a root preserve IDs using filesystem identity. New corrupt
media aborts a scan transaction. Imported audio that already has zero duration is
retained and counted as invalid. Uploads spool privately, validate every audio
file, publish a new directory atomically, and refuse existing destinations.

A read-only-media rehearsal scanned all three book libraries in the copied
catalog: 24 + 1 + 1,481 books, no newly created book records and no missing books.
It retained one pre-existing zero-duration MP3 with invalid MPEG headers. This
file has not been repaired or removed. Two podcast items' five missing source
records from the import remain outside these book scans.

A disposable real Chromium test now verifies playback, pause/save, metadata
editing, cover upload, scanning, uploading a new book and browsing it. It uses
only generated audio and disposable accounts/databases, not the live household
library. Run it after building the web assets, with a local Chrome CDP endpoint:

```sh
POCKET_NATIVE_BROWSER_CDP=http://127.0.0.1:9222 \
  go test ./internal/server -run TestNativeBrowserAcceptance -v
```

Add `POCKET_NATIVE_REHEARSAL_SCAN=1` to the snapshot rehearsal command to exercise
scanning against a disposable database copy. Source media remains read-only.
FFprobe must be on PATH. The native backend remains opt-in and has not been
switched on in production. All pending rows in the feature matrix still apply.

## Recoverable deletion and podcast retention

Native deletion records a durable journal before moving indexed files beneath
`.pocket-trash` in each media root. Rename operations never overwrite an existing
file. Restore checks the original file identity and refuses conflicting new files;
purge is a separate, confirmed irreversible operation. Personal progress/history
rows survive deletion. Shared files referenced by another item are left in place.
Catalog-only removal leaves all media at its original paths. Scans respect deleted
item tombstones, including same-root moves, and ignore hidden trash directories.
Keep media roots and their `.pocket-trash` folders with the database when backing up.

The web book/podcast menus and Trash page expose removal, restore, retry and purge.
Client `DELETE /api/items/:id?hard=1` also moves files to recoverable trash; omitting
`hard=1` removes only the catalog entry. Batch deletion returns individual results.
The native API requires an item revision, and permanent purge requires explicit
confirmation. Delete access is checked against the current account and library.

Podcast keep-latest settings apply to downloaded episodes, ordered by publication
time. Older episodes move to trash; recent active sessions protect an episode.
Retention runs after downloads, due feed checks and settings changes. A trashed
episode is not automatically queued again. Manual redownload retains episode ID
and progress; restoring its older trash entry cannot replace that newer copy.
Trash consumes disk until purged, including episodes removed by retention.

Synthetic tests verify permissions, stale revisions, partial-move recovery,
restore collisions, changed-file refusal, purge, scanner tombstones, active-session
retention protection and redownload identity. Real media is not used in deletion tests.

## Podcast editing and client contracts

Episode edits have optimistic revisions and separate override records, so a feed
refresh preserves corrected titles, descriptions, numbering, dates, chapters and
enclosure URLs. Downloaded media, episode IDs and listening positions are retained.
Text edits strip active HTML. Playback never follows an edited URL directly;
downloads use the same vetted network transport, size limits and media validation.

Apple Podcasts discovery uses the documented [iTunes Search API](https://developer.apple.com/library/archive/documentation/AudioVideo/Conceptual/iTuneSearchAPI/),
with explicit-content filtering, bounded responses and a direct RSS URL fallback.
The UI links to the directory listing and does not embed promotional previews or artwork.

Native client routes now handle podcast feed previews, creation, OPML parsing,
episode searches/checks, download queues/cancellation and episode read/edit/delete.
Unsupported operations still return an explicit error. Imported date strings are
normalized to client milliseconds. The owner-only snapshot rehearsal verifies all
11 shows and 77 episode records without running downloads or modifying media.
Browser checks cover episode edit, feed refresh and recoverable deletion/restore.
