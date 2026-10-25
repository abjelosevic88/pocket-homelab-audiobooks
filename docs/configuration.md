# Configuration

Everything is read from environment variables; every value has a default, so a bare `docker run -v ./data:/data` works.
Integration values seed the owner's (first admin's) own settings on first start, once; after that the values saved under
**Settings** win, and clearing one there keeps it cleared.

## Server

| Variable | Default | What it does |
|---|---|---|
| `PORT` / `HOST` | `8080` / `0.0.0.0` | Listen address |
| `BASE_URL` | – | Public URL: notification links, the OIDC callback, what Pocket Core shows |
| `DATA_DIR` | `./data` (`/data` in the image) | SQLite database, `secret.key`, backups |
| `DB_PATH` | `$DATA_DIR/pocket-audiobooks.db` | Override the database location |
| `TZ` | – | Time zone for "today", streaks and the weekly recap |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `text` | `debug`, `warn`, `error`; `json` |
| `TRUST_PROXY` | `true` | Honour `X-Forwarded-For` / `X-Real-IP`, but only from a peer in `TRUSTED_PROXIES` |
| `TRUSTED_PROXIES` | loopback + private ranges | Comma-separated CIDRs or IPs of your reverse proxies (`127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7`) |
| `METRICS_ENABLED` | `false` | Prometheus `/metrics` (row counts only, never content); readable by admins or with `METRICS_TOKEN` |
| `METRICS_TOKEN` | – | Bearer token a scraper sends to `/metrics` |
| `USER_AGENT` | `PocketAudiobooks/1.0 (+repo URL)` | Sent with outgoing requests |

## Sign-in

| Variable | Default | What it does |
|---|---|---|
| `AUTH_MODE` | `local` | `local` (built-in accounts), `proxy` (trust `AUTH_PROXY_HEADER`), `none` (single user, LAN only) |
| `AUTH_PROXY_HEADER` / `AUTH_PROXY_EMAIL_HEADER` | `Remote-User` / `Remote-Email` | Headers for proxy mode |
| `SESSION_TTL` | `720h` | Cookie lifetime |
| `SECURE_COOKIES` | `false` | Set `true` behind HTTPS (on plain http the browser drops the cookie and login bounces) |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` / `ADMIN_NAME` | – | Create the first admin on an empty database (otherwise the UI asks) |
| `OIDC_ISSUER` / `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` | – | Sign in with an OpenID Connect provider (PocketID, Authelia, Authentik, Keycloak…). The callback is `BASE_URL/api/v1/auth/oidc/callback`. With Pocket Core none of these are needed. |
| `OIDC_NAME` / `OIDC_AUTO_CREATE` | `single sign-on` / `true` | Button label; give an unknown verified email a member account |

## Library server

`MEDIA_BACKEND=native` is the default. Audio, ebooks and imported artwork live in
mounted folders; library metadata and personal state live in SQLite. Add a mounted
folder under Settings → Libraries, scan it, and optionally enable periodic scans.
FFmpeg and FFprobe are included in the container (`FFMPEG_PATH` and `FFPROBE_PATH`
can override executable paths). The process needs read access to the media and
write access for audiobook uploads, Trash and metadata tools.

Converted (HLS) playback writes its segments to `DATA_DIR/cache/hls`, never to
`/tmp`; the directory is emptied at start and kept under `HLS_CACHE_MB` (default
1024). `HLS_ENCODERS` (default 4) bounds concurrent conversions for signed-in
listeners and `HLS_SHARE_ENCODERS` (default 2) those of public shares, which
have their own pool so they cannot starve the owner.

`MEDIA_BACKEND=audiobookshelf` retains the historical gateway for migration
rollback. Only that mode uses `AUDIOBOOKSHELF_URL`, `AUDIOBOOKSHELF_PUBLIC_URL`,
`AUDIOBOOKSHELF_TOKEN` or `AUDIOBOOKSHELF_USER`/`AUDIOBOOKSHELF_PASSWORD`, and
`SYNC_EVERY`. Native mode does not connect to Audiobookshelf or use its credentials.

## Personal integrations

| Variable | Default | What it does |
|---|---|---|
| `NOTIFY_URL` / `NOTIFY_FORMAT` | – / guessed | The owner's Apprise `…/notify/<key>`, ntfy topic URL or JSON webhook: finished books, the weekly recap. Everyone sets their own under Settings → Notifications. |

## Pocket Core

| Variable | Default | What it does |
|---|---|---|
| `CORE_URL` / `CORE_REGISTRATION_KEY` | – | [Pocket Core](https://github.com/abjelosevic88/pocket-homelab-core): register on start (the key is needed once; the app token is kept in `$DATA_DIR/core.token`), "Sign in with Pocket Core", accept the tokens Core gives paired phones, publish `audiobooks.finished`, offer `listening:read` to other Pocket apps (Pocket Day, Pocket Bookshelf) |
| `CORE_APP_URL` | `BASE_URL` | How Core reaches this app for health checks (e.g. `http://host.docker.internal:8202`) |

With `CORE_URL` empty the app behaves exactly as a standalone app: every Core-only choice stays visible, greyed out
with a note.

## Audiobook uploads

`MAX_AUDIOBOOK_UPLOAD_BYTES` caps one multipart audiobook upload (default `10737418240`, 10 GiB). This is separate
from `MAX_UPLOAD_BYTES`, the smaller limit used for data imports. Native uploads validate media in a private staging folder before publishing a new book. The
signed-in person needs upload permission. Allow an equivalent request size and sufficiently
long upload timeouts in the reverse proxy, and free space in the destination library.

### Native metadata providers

In native mode, Libraries → Metadata providers configures Audible (default, no
key; details and chapters via [Audnexus](https://audnex.us)), Open Library, Google
Books (requires your API key), and custom providers using the
`GET /search?query=…&author=…&isbn=…&mediaType=book` / `{ "matches": […] }` contract.
Provider credentials are sealed with the app's `secret.key`; include it in backups.
Metadata is previewed in the editor before saving. Cover URLs save only when
explicitly selected, with the same image validation as uploaded covers.

Outbound metadata/artwork requests permit public HTTP/HTTPS destinations by
default. For a self-hosted provider, set `NATIVE_FETCH_PRIVATE_HOSTS` to its exact
hostname (comma-separated for multiple hosts). DNS and each redirect are checked
at connection time; provider credentials are not forwarded to a different host.

`AUDIBLE_REGION` (default `us`; one of `us ca uk au fr de jp it in es`) selects the
Audible store when the provider's own region setting is left on Default. Each
provider has a match order: **Match** (book page) and **Quick match** (book page,
library toolbar or a selection) try enabled providers from the lowest number up.
Requests to Audible and Audnexus are spaced about one second apart, shared by all
users and bulk jobs, so a quick match of 1,500 books takes roughly an hour. Quick
match fills only empty fields unless its options say otherwise; see [API](api.md#metadata-match).

Provider contracts: [Open Library search](https://openlibrary.org/dev/docs/api/search)
and [Google Books API](https://developers.google.com/books/docs/v1/using).

### Native ebook reader

Books with EPUB or PDF files offer **Read ebook**. The reader includes chapter
navigation, text size, page controls and saved positions. EPUB locations use CFIs,
and primary ebook positions share the existing client progress contract. Companion
files keep separate positions. Opening a book never resets its audio position.
Download permission is required to read or retrieve an ebook.

The browser keeps unacknowledged reading positions per account and retries them.
If another device changed the position, the reader offers the server position
instead of overwriting it silently. Account changes invalidate pending writes.

EPUB originals are unchanged. Go prepares a private derived archive, strips active
content and external URLs, and supplies a restrictive content policy; the browser
renders it in an iframe with scripts disabled. Archives are limited to 128 MiB
compressed, 256 MiB expanded, 32 MiB per entry and 10,000 entries. Standard IDPF/Adobe font obfuscation is decoded in the derived copy.
DRM-encrypted content requires an external reader. PDF.js renders PDF pages
and selectable text in the browser. Neither reader starts a separate server or
uses Audiobookshelf. Reader dependencies load only on the reader page; notices
are at `/reader-licenses.txt`.

### Native shares and RSS

Administrators with download permission can create **Share & RSS** links from
books, series or collections. **Shares & RSS** lists
links and lets their publisher change passwords, expiry and download options, or
revoke them. `BASE_URL` must be the canonical HTTP(S) address used by recipients.
New web shares default to seven days; no expiry is also supported.

Links use random slugs by default. Anyone with an unprotected link can access its
contents. Optional passwords use HTTP Basic authentication (any username), with
hashed storage, throttled failures and signed browser grants. Password changes
invalidate old grants. Collection and series shares follow their current contents;
every media request checks the publisher's current account and library permissions.
RSS enclosures contain original files; multi-file books have one feed entry per
part. Indexing is discouraged by HTTP and RSS directives, not relied on for access
control. A downloadable file already obtained by a recipient cannot be revoked.

Public pages play through files, save a place locally in the recipient's browser,
and fall back to the local FFmpeg/HLS converter when needed. Shared playback does
not write progress, history or listening sessions into the publisher's account.
Book archive and companion-file links appear only when downloads are enabled;
RSS clients can save the audio they are allowed to stream regardless of this option.
No shares are created automatically during deployment.
