# API

JSON over HTTP at `/api/v1`. Authenticate with the session cookie (the web app), a personal token from
Settings → API & shortcuts (`Authorization: Bearer pab_…`), a token Pocket Core gave a paired phone, or a Pocket Core
app-link token (`pcx_…`, limited to the scopes below). Everything answers for the person the token belongs to; other
people's ids answer 404. Times are UTC RFC 3339 unless they are the compatibility API's milliseconds (`*_at` numbers);
days are the server's local `YYYY-MM-DD`.

The default native backend serves the local catalog and personal listening state. The optional legacy backend requires a separate Audiobookshelf connection.

A **book** is a native library item trimmed to what a screen needs — `id`, `library_id`, `title`, `subtitle`,
`author`, `narrator`, `series` ("Name #2"), `sequence`, `authors` / `series_list` (`{id, name, sequence}`), `genres`,
`year`, `duration` (seconds), `cover` (a Pocket Audiobooks URL), `chapters`, and the person's own `progress` (0–1),
`current_time`, `finished`, `last_played`, `finished_at`. The book page adds `description`, `narrators`,
`chapter_list`, `publisher`, `language`, `format`, `files`, `size`.

## Library

| | |
|---|---|
| `GET /home` | `continue` (in progress), `series_next`, `up_next`, `recent`, `discover` (not started), `listen_again`, `authors`, `libraries`, `today` (`seconds`, `week_seconds`, `goal_minutes`, `streak`) |
| `GET /libraries?all=1` | The person's book libraries (podcast libraries are left to Pocket Podcasts); `all` includes hidden ones, with counts |
| `GET /books?library=&sort=&desc=1&show=&genre=&narrator=&page=&limit=` | One library's books. `sort` = `added` `title` `author` `year` `length` `progress` `random`; `show` = `in-progress` `not-started` `finished` `not-finished` |
| `GET /books/{id}` | `{book, bookmarks, series:[{id, name, books}], up_next, listened:{seconds, sessions, first}, open_url}` |
| `POST /books/{id}/progress` | `{"action":"finished\|unfinished\|reset\|hide"}` or `{"action":"position","time":seconds}` — saved in the native catalog |
| `POST /books/{id}/bookmarks {"time","title"}` · `PUT …` (rename) · `DELETE …?time=` | Bookmarks shared with connected native clients |
| `GET /series?library=&sort=name\|added\|books\|length` · `GET /series/{id}?library=` | Series with covers and how far you are; one series' books in order |
| `GET /authors?library=` · `GET /authors/{id}` | Authors; one author with their books |
| `GET /search?q=` | `{books, authors, series, narrators}` across the libraries |
| `GET /cover/{id}?w=` · `GET /author-image/{id}?w=` | Permission-checked cover and author images |

## Playing

| | |
|---|---|
| `POST /play/{id}` | `{"mime_types":["audio/mpeg","audio/mp4",…],"client":"<browser id>","device":"Firefox on macOS","force_transcode":false}` → opens a native playback session as the person: `session_id`, `method` (`direct` / `hls`), `tracks` (`url` under `/api/v1/media/`, `start` and `duration` in book time, `mime`), `current_time` (their place), `duration`, `chapters`, `book`, `speed` |
| `POST /sessions/{sid}/sync` | `{"current_time","listened","duration","playing","item_id","client"}` every 15 seconds and on pause; `"close":true` when stopping, `"ended":true` at the end. Saves the person's place locally; native sessions survive restarts |
| `GET /next?after=<id>` | What plays after a book: Up next first, else the next book of its series |
| `GET /now-playing?client=&session=` | What the person plays elsewhere: another browser, or a connected native client (their newest listening session): `active`, `playing`, `item_id`, `title`, `position`, `device`, `app`, `updated_at` |
| `GET /media/api/items/{id}/file/{ino}` · `GET /media/hls/{sid}/{file}` | Local files and FFmpeg HLS playlists/segments, with ownership checks and byte ranges. One continuous FFmpeg encoder per session produces the segments (gapless boundaries); it runs a few minutes ahead, restarts on far seeks and stops when the session closes or stays idle for 2 minutes |
| `GET /events` | Live updates (Server-Sent Events, one JSON object per `data:` line): `progress` (another device moved a place), `session` (a session opened/closed), `items` (`updated`/`added` ids after edits, matches and scans), `scan_complete`. Only the person's own events and books they can read. Audiobookshelf apps get the same over Socket.IO (`/socket.io/`, polling with WebSocket upgrade): `user_item_progress_updated`, `item_updated`/`items_updated`, `item_added`/`items_added`, `user_session_closed`, `scan_complete` |

## Up next

| | |
|---|---|
| `GET /up-next` | Your queue, in order |
| `POST /up-next {"item_id","first":false}` · `PUT /up-next {"item_ids":[…]}` · `DELETE /up-next/{itemId}` | Add, reorder, remove |

## History and stats

| | |
|---|---|
| `GET /history?from=&to=&book=&q=&limit=&offset=` | `{items, total}`, newest first — one row per listening session (web and native client sessions) with `seconds`, `start_pos`, `end_pos`, `device`, `source` (`pocket`, `audiobookshelf`, `import`) |
| `DELETE /history/{id}` | Forget one session from listening history |
| `GET /finished?from=&to=` | Finished books |
| `GET /day/2026-10-24` | One day: `books` (time per book, from→to, devices), `finished`, `seconds`, `summary` ("1h 20m of Dracula, finished The Time Machine") |
| `GET /stats?range=7d\|30d\|90d\|365d\|all\|2026` | Totals, time per day / week / month, finished books, top books, authors, narrators, genres, devices, weekdays, hours, streaks |
| `GET /dashboard` | Today and this week in seconds, books this week, finished this year, the current book, what is playing now |
| `GET /sync` · `POST /sync` · `POST /sync?full=1` | Reconcile native session history: state; now; everything again |

## Pocket Core app links

Pocket Audiobooks offers one scope to other Pocket apps (switched on per person in Pocket Core):

| Scope | Routes |
|---|---|
| `listening:read` | `GET /dashboard`, `/history`, `/stats`, `/finished`, `/day/{date}`, covers |

Event published on the Core bus (with `user_email`): `audiobooks.finished` (a book finished, here or in an
Audiobookshelf app).

## Homepage widget

```yaml
- Pocket Audiobooks:
    href: https://audio.example.com
    widget:
      type: customapi
      url: https://audio.example.com/api/v1/dashboard
      headers: { Authorization: Bearer pab_… }
      mappings:
        - { field: week_seconds, label: This week, format: duration }
        - { field: week_books, label: Books }
        - { field: finished_this_year, label: Finished }
```

## Everything else

Sign-in (`/auth/*`), people (`/users`), personal tokens (`/tokens`), settings (`/settings`), `POST /backup` (admins;
`GET` also works with a bearer token or from the app's own pages, keeping the newest five), `GET /export.json` and
`POST /import` (your own history, finished books and Up next), `/healthz`, `/readyz`, `/metrics` (off by default).

Browser requests that change something (POST/PUT/PATCH/DELETE without a bearer token or `X-API-Key`) must come
from this app's own origin (`Origin`/`Referer` matching the host or `BASE_URL`); other sites get `403`.

## Pocket Homelab / Audiobookshelf gateway

The native player connects to the app's base URL (no `/api/v1` suffix), with a Pocket Audiobooks `pab_` personal
token or a Pocket Core device token. HTTP Bearer and `X-API-Key` authentication work; GET/HEAD media requests
also accept `?token=`. Cookie, anonymous, proxy-header and app-link authentication do not grant gateway access.

The gateway preserves Audiobookshelf's JSON shapes and paths: `/status`, `/ping`, library browsing/search,
items, covers, authors, series, `/api/me` and listening stats, play/sync/close sessions, progress, bookmarks,
byte-range files, downloads, HLS playlists/segments, local session sync, and the upload/delete operations used
by Pocket Homelab (subject to the person's Audiobookshelf permissions). `/api/v1` remains Pocket Audiobooks' web API.

With the default native backend, requests use the local catalog and the person's native permissions. Sessions and media are checked against the signed-in user. Socket.IO supports local playback and library updates. Unsupported operations return errors rather than falling through to another server.

Compatible third-party clients can use `POST /login` with a Pocket email and an app password created in Settings → Apps & API. `/api/authorize`, `/auth/refresh` and `/logout` support token authentication. Native routes also answer under `/audiobookshelf/…`. OpenID sign-in on this compatibility interface is not offered. The Pocket Homelab app uses its Core device token directly.

## Uploads

`GET /api/v1/upload` returns `{allowed, max_bytes, libraries}` for the signed-in person, including permitted destination folders. `POST /api/v1/upload` accepts multipart data: `library`, `folder`, `title`, optional `author` and `series`, and numbered file parts. `/api/upload` accepts compatible native-client uploads.

Native uploads validate account and folder permissions, stream to staging files, and publish without overwriting an existing destination. Size limits apply even without Content-Length. Files are indexed locally. Oversized requests return 413 and unauthorized accounts return 403.

## Native catalog

These endpoints read the independent local SQLite catalog. The default `MEDIA_BACKEND=native` also dispatches web and playback-client routes to the local engine. See [standalone migration](standalone-migration.md).

All require normal Pocket authentication (session, API token, or a validated Core
person/device token) plus an explicitly mapped native account. Core app-link
scopes do not grant access to these preview routes. Library, tag and explicit
content restrictions apply to lists, counts, detail, progress and files. An
unmapped, inactive or locked source account has no access, including Pocket admins.

| Method | Path | Result |
| --- | --- | --- |
| GET | `/catalog/libraries` | Accessible libraries with permission-filtered item counts |
| GET | `/catalog/items?library=&type=&q=&offset=0&limit=50` | Stable title/ID order, literal title/author substring search, maximum 200 items per page |
| GET | `/catalog/items/{id}` | Imported item metadata and indexed file descriptions |
| GET | `/catalog/me/progress` | The account's imported progress for currently accessible items |
| GET, HEAD | `/catalog/items/{id}/files/{file}` | Local file bytes with byte-range support; `?download=1` requires download permission |

Paths are relative to `/api/v1`. Files open within configured media roots using
Go's `os.Root`; symlink escapes are blocked. Only audio and raster covers render
inline. Ebooks and supplementary files are attachments and require download
permission. All responses use private, no-store caching. A missing/restricted
entry returns 404; absent Pocket authentication returns 401.

### Native library administration

These routes require `MEDIA_BACKEND=native` and the mapped account's permissions.
Creating libraries additionally requires a Pocket administrator. Directory paths
refer to folders already mounted into the container.

| Method | Path | Result |
| --- | --- | --- |
| GET | `/catalog/manage` | Libraries, scan permissions, watch settings and recent scan jobs |
| POST | `/catalog/libraries` | Create a library with `name`, `media_type`, `path` |
| POST | `/catalog/libraries/{id}/scan` | Start a background scan; `force:true` reprobes metadata |
| PUT | `/catalog/libraries/{id}/watch` | Set `enabled` and `interval_seconds` (60–86400) |
| DELETE | `/catalog/scans/{job}` | Cancel a scan; its catalog transaction remains unapplied |
| GET, PUT | `/catalog/items/{id}/editor` | Metadata/chapters with `expected_updated_at` conflict protection |
| POST | `/catalog/items/{id}/cover` | Upload `file` as a JPEG/PNG cover |
| POST | `/upload` | Stream multipart files into a validated new book; refuses existing destination |

### Metadata match

Requires metadata editing permission. Providers are tried in match order (Settings → Metadata providers);
`provider=` restricts a request to one. Searching never changes a book.

| Method | Path | Result |
| --- | --- | --- |
| GET | `/catalog/metadata/providers` · PUT (admin) | Providers with `kind` (`audible`, `openlibrary`, `google`, `custom`), `region` (Audible), `priority` (match order, lowest first) |
| GET | `/catalog/metadata/search?provider=&q=&author=&isbn=` | Raw provider results; `provider=audible&asin=` returns one Audnexus detail record |
| GET | `/catalog/items/{id}/match?provider=&title=&author=` | `{item:{metadata, expected_updated_at, has_cover, duration}, query, provider, candidates:[{id, provider, title, authors, cover, duration, metadata, score:{total, title, author, duration, exact, confident}}], errors}`. Title/author default to the cleaned book title and first author; an ASIN on the book is looked up directly |
| POST | `/catalog/items/{id}/match` | `{expected_updated_at, metadata:{…chosen fields}, cover:"url"}` — applies through the editor (409 when stale); the cover is downloaded and validated before anything changes |
| POST | `/catalog/items/{id}/quick-match` | `{provider?, override_details, override_cover, update_authors, update_series}` → `{status: matched\|skipped\|low_confidence\|not_found\|failed, fields, cover, match_title, score, exact, message}` |
| GET | `/catalog/items/{id}/audible-chapters?asin=&region=` | Audible chapter markers via Audnexus, clamped to the book: `{chapters, runtime, duration, difference, mismatch}` (`mismatch` when runtimes differ by more than 2 minutes). Nothing is saved |
| POST | `/catalog/match-jobs` | Bulk quick match in the background: `{library_id}` or `{ids:[…≤1000]}` plus the quick-match options → `202 {id, total}`; one running job per person (409) |
| GET | `/catalog/match-jobs?library=` · `/catalog/match-jobs/{job}?status=&offset=&limit=` | Recent jobs with `status`, `done`/`total`, `counts` per result status; one job with per-book `results` |
| DELETE | `/catalog/match-jobs/{job}` | Cancel a running job (the book in progress is left unchanged), or delete a finished job's report |

Quick match applies a result only when it is confident: the same ASIN/ISBN, or title similarity ≥ 0.9 with a matching
author (≥ 0.8) and, when both runtimes are known, a runtime within about 25%. By default it only fills empty fields and
adds a missing cover; the options allow replacing details, covers, authors or series. Tags and the explicit flag are never
changed by quick match because they can affect library access.

### Author profiles

Like Audiobookshelf's author match: Audnexus (`api.audnex.us`, the Audible provider's region, the shared ~1 request/s
limit) supplies an author's ASIN, biography (HTML converted to plain text) and photo. An author is visible when one of
the person's books credits it; changes need metadata editing permission. Photos are stored as content-addressed native
artwork (JPEG, PNG or WebP, up to 10 MB and 40 megapixels) and included in application bundles; photos imported from
Audiobookshelf's metadata folder keep being served from the read-only mount.

| Method | Path | Result |
| --- | --- | --- |
| GET | `/catalog/authors/{id}` | `{id, name, asin, description, image, has_image, last_matched, updated_at, books, can_edit}`; `image` is a versioned photo URL or `""` |
| PUT | `/catalog/authors/{id}` | `{name?, description?, asin?, remove_image?, image_url?}` → the author. A new name is written to every book crediting the author (409 when another author has it); `image_url` is downloaded through the guarded fetcher |
| GET/HEAD | `/catalog/authors/{id}/image?v=` | The photo with its own content type; `v=` URLs are `private, immutable`, native photos carry an `ETag` |
| POST · DELETE | `/catalog/authors/{id}/image` | Upload multipart `file` as the photo, or remove it → the author |
| POST | `/catalog/authors/{id}/match` | `{asin?, name?}` → `{result:{status: matched\|skipped\|not_found\|failed, asin, match_name, distance, fields, image, message}, author}`. An ASIN is looked up directly; otherwise the author's known ASIN, then the closest name in the Audnexus search (at most 3 edits, fewer for very short names). A single match replaces the biography and photo; `failed` answers 502 |
| POST | `/catalog/author-match-jobs` | `{library_id, only_missing:true}` → `202 {id, total}`. With `only_missing` (default) only authors lacking a photo or biography are visited and existing ones are kept. Shares the one-running-match-job-per-person rule with book quick match (409) |
| GET | `/catalog/author-match-jobs?library=` · `/catalog/author-match-jobs/{job}?status=&offset=&limit=` | Recent author jobs (`status`, `done`/`total`, `counts`, `options.only_missing`); one job with per-author `results` (`author_id`, `name`, …as above) |
| DELETE | `/catalog/author-match-jobs/{job}` | Cancel a running job, or delete a finished job's report |

The Audiobookshelf-compatible `/api/authors/{id}` (used by the iOS apps) includes `asin`, `description`, `imagePath` and
an `updatedAt` that changes with the profile, and `/api/authors/{id}/image` serves the same photo.

Native web playback reports accept `report_id`, `observed_at`, `current_time`,
`listened`, `playing`, `close` and `ended`. Stable report IDs make retries
idempotent. The compatibility `/api/session/{id}/sync` accepts `reportId` and
`reportedAt` alongside the usual playback fields. Older clients without these
fields still work, but delta-only retries cannot guarantee exact accounting.

### Native collections and downloads

With the native backend, `/api/collections` and `/api/playlists` support list/create,
`/{id}` supports read/update/delete, and book/item and batch add/remove routes keep
ordered contents. Collections are shared within a library and require update
permission to edit; playlists belong to their creator. Hidden entries cannot be
silently removed by a restricted editor. Updates accept an optimistic `revision`.
The web app uses equivalent authenticated `/api/v1/organize/*` routes.

`GET|HEAD /api/items/{id}/download` and
`GET|HEAD /api/v1/catalog/items/{id}/download` return a streamed ZIP of original
files, with download permission enforced independently from playback.

Native `/api/session/local` and `/api/session/local-all` accept cumulative offline
listening sessions. Session IDs are scoped to the authenticated person; retries
and reordered totals do not add listening twice. Old offline uploads cannot
replace a newer online position or manual progress edit. Imported sessions are
reconciled transactionally, retaining their original record as audit data.

### Audiobook-only scope

Podcast routes are unavailable (404), podcast library creation is rejected, and
library/item/media/progress APIs expose only books. Old podcast libraries are
archived on upgrade and their download jobs disabled. Their source files and
recovery records are retained; no podcast scheduler runs. New imports omit podcast
libraries, episodes and related personal state.

## Native sharing

- `GET|POST /api/v1/shares`; `PUT|DELETE /api/v1/shares/{id}`: the administrator's
  shares. Creation accepts `kind` (`item`, `series`, `collection`), `target_id`,
  `name`, optional `password`, `downloadable`, and
  `expires_at` (Unix milliseconds, zero for no expiry). Updates require `revision`.
  Omit `password` to retain it; an empty password removes protection.
- `GET|HEAD /share/{slug}`: public player; `/feed/{slug}`: RSS; media paths beneath
  the share revalidate its membership, password, expiry and publisher permissions.
- Compatibility: `GET /api/feeds`, `POST /api/feeds/{item|series|collection}/{id}/open`,
  `POST /api/feeds/{id}/close`, `POST /api/share/mediaitem`,
  `DELETE /api/share/mediaitem/{id}`. Feed URLs use configured `BASE_URL`, not an
  untrusted client-supplied host.

## Native ebook reading

- `GET /api/v1/catalog/items/{id}/reader?file={file_id}` returns permitted EPUB/PDF
  files and the selected file's position/revision. Omit `file` for the primary.
- `PUT` to that route saves `{user_id, location, progress, revision}`. Progress is
  a fraction; EPUB locations are CFIs and PDF locations are page-number strings.
  Stale positions return 409; identical retries succeed.
- `GET|HEAD /api/v1/catalog/items/{id}/reader/{file_id}` serves a prepared EPUB or
  original PDF for the browser reader. Original ebook downloads remain available
  through the catalog/client file routes.
