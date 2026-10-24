# API

JSON over HTTP at `/api/v1`. Authenticate with the session cookie (the web app), a personal token from
Settings → API & shortcuts (`Authorization: Bearer pab_…`), a token Pocket Core gave a paired phone, or a Pocket Core
app-link token (`pcx_…`, limited to the scopes below). Everything answers for the person the token belongs to; other
people's ids answer 404. Times are UTC RFC 3339 unless they are Audiobookshelf's own milliseconds (`*_at` numbers);
days are the server's local `YYYY-MM-DD`.

A person without a working Audiobookshelf connection gets `409 {"code":"not_connected","error":"…"}` from anything
that needs Audiobookshelf.

A **book** is Audiobookshelf's library item trimmed to what a screen needs — `id`, `library_id`, `title`, `subtitle`,
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
| `POST /books/{id}/progress` | `{"action":"finished\|unfinished\|reset\|hide"}` or `{"action":"position","time":seconds}` — written to Audiobookshelf |
| `POST /books/{id}/bookmarks {"time","title"}` · `PUT …` (rename) · `DELETE …?time=` | Bookmarks, kept in Audiobookshelf (the phone apps see them) |
| `GET /series?library=&sort=name\|added\|books\|length` · `GET /series/{id}?library=` | Series with covers and how far you are; one series' books in order |
| `GET /authors?library=` · `GET /authors/{id}` | Authors; one author with their books |
| `GET /search?q=` | `{books, authors, series, narrators}` across the libraries |
| `GET /cover/{id}?w=` · `GET /author-image/{id}?w=` | Pictures, resized by Audiobookshelf |

## Playing

| | |
|---|---|
| `POST /play/{id}` | `{"mime_types":["audio/mpeg","audio/mp4",…],"client":"<browser id>","device":"Firefox on macOS","force_transcode":false}` → opens an Audiobookshelf playback session as the person: `session_id`, `method` (`direct` / `hls`), `tracks` (`url` under `/api/v1/media/`, `start` and `duration` in book time, `mime`), `current_time` (their place), `duration`, `chapters`, `book`, `speed` |
| `POST /sessions/{sid}/sync` | `{"current_time","listened","duration","playing","item_id","client"}` every 15 seconds and on pause; `"close":true` when stopping, `"ended":true` at the end. Moves the person's place in Audiobookshelf. If Audiobookshelf forgot the session (a restart) the place is saved anyway and the answer carries a new `session_id` |
| `GET /next?after=<id>` | What plays after a book: Up next first, else the next book of its series |
| `GET /now-playing?client=&session=` | What the person plays elsewhere: another browser, or any Audiobookshelf app (their newest listening session): `active`, `playing`, `item_id`, `title`, `position`, `device`, `app`, `updated_at` |
| `GET /media/api/items/{id}/file/{ino}` · `GET /media/hls/{sid}/{file}` | The files and HLS playlists/segments, passed to Audiobookshelf with the person's own key (byte ranges included). Only these paths. |

## Up next

| | |
|---|---|
| `GET /up-next` | Your queue, in order |
| `POST /up-next {"item_id","first":false}` · `PUT /up-next {"item_ids":[…]}` · `DELETE /up-next/{itemId}` | Add, reorder, remove |

## History and stats

| | |
|---|---|
| `GET /history?from=&to=&book=&q=&limit=&offset=` | `{items, total}`, newest first — one row per listening session (this app's and every Audiobookshelf app's) with `seconds`, `start_pos`, `end_pos`, `device`, `source` (`pocket`, `audiobookshelf`, `import`) |
| `DELETE /history/{id}` | Forget one session here (Audiobookshelf keeps its own) |
| `GET /finished?from=&to=` | Finished books |
| `GET /day/2026-10-24` | One day: `books` (time per book, from→to, devices), `finished`, `seconds`, `summary` ("1h 20m of Dracula, finished The Time Machine") |
| `GET /stats?range=7d\|30d\|90d\|365d\|all\|2026` | Totals, time per day / week / month, finished books, top books, authors, narrators, genres, devices, weekdays, hours, streaks |
| `GET /dashboard` | Today and this week in seconds, books this week, finished this year, the current book, what is playing now |
| `GET /sync` · `POST /sync` · `POST /sync?full=1` | Reading history from Audiobookshelf: state; now; everything again |

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

Sign-in (`/auth/*`), people (`/users`), personal tokens (`/tokens`), settings (`/settings`), `GET /backup` (admins),
`GET /export.json` and `POST /import` (your own history, finished books and Up next), `/healthz`, `/readyz`, `/metrics`.

## Pocket Homelab / Audiobookshelf gateway

The native player connects to the app's base URL (no `/api/v1` suffix), with a Pocket Audiobooks `pab_` personal
token or a Pocket Core device token. HTTP Bearer and `X-API-Key` authentication work; GET/HEAD media requests
also accept `?token=`. Cookie, anonymous, proxy-header and app-link authentication do not grant gateway access.

The gateway preserves Audiobookshelf's JSON shapes and paths: `/status`, `/ping`, library browsing/search,
items, covers, authors, series, `/api/me` and listening stats, play/sync/close sessions, progress, bookmarks,
byte-range files, downloads, HLS playlists/segments, local session sync, and the upload/delete operations used
by Pocket Homelab (subject to the person's Audiobookshelf permissions). `/api/v1` remains Pocket Audiobooks' web API.

Each request uses the person's configured Audiobookshelf connection. Backend access/legacy tokens are replaced
in responses and refresh tokens are removed. HLS and playback sessions are checked against the person's ABS user,
even for an ABS administrator. Unknown operations, user administration and key management are rejected.

Third-party Audiobookshelf apps (the Audiobookshelf app, AudioBooth, Plappa, ShelfPlayer…) work too: `/status`
offers username/password sign-in, and `POST /login` takes the person's email with an **app password** (a `pab_`
token, Settings → Apps & API → Audiobookshelf apps) — or their Pocket Audiobooks password, which gets the app its
own token. `POST /api/authorize`, `POST /auth/refresh` and `POST /logout` answer like Audiobookshelf; socket.io
(`/socket.io/`, websocket and polling) is relayed with the app's token swapped for the person's Audiobookshelf one in
the `auth` event and back. Every native route also answers under `/audiobookshelf/…` for apps that expect that
path. OpenID sign-in is not offered.

## Uploads

`GET /api/v1/upload` returns `{allowed, max_bytes, libraries}` for the signed-in person. Only book libraries are
listed, including their `folders` (`id`, `fullPath`). `allowed` comes from their Audiobookshelf upload permission.

`POST /api/v1/upload` accepts streaming multipart form data: required `library`, `folder`, `title`, optional
`author` and `series`, and numbered file parts (`0`, `1`, …). The native gateway's `/api/upload` uses the same
streaming implementation. Files are forwarded as the person's account; Audiobookshelf validates the library
and folder access. No complete-file buffering in Pocket memory or its data volume, and no short API timeout.
Success is Audiobookshelf's `200 OK`; scanning may finish later. Matching destination filenames are overwritten.
The configured whole-request size limit applies even without Content-Length. Oversized requests return 413;
accounts without upload permission return 403 before any file bytes are forwarded.
