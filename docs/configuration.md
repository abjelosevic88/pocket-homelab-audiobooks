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
| `TRUST_PROXY` | `true` | Honour `X-Forwarded-For` |
| `METRICS_ENABLED` | `true` | Prometheus `/metrics` (row counts only, never content) |
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

## Audiobookshelf

Every person listens with their **own** Audiobookshelf account, so their place in each book, finished marks and
bookmarks stay theirs and the Audiobookshelf phone apps agree with the web. Under Settings → Audiobookshelf they
either paste an **API key** made for their account (Audiobookshelf → Settings → API Keys, by an admin) or sign in
with their **password** (only the refresh token Audiobookshelf hands out is kept, never the password). Both are
sealed with `DATA_DIR/secret.key`. With Pocket Core, Core can hold the login instead (service type
`audiobookshelf`: `TOKEN`, plus `USER_ID` so a box-wide admin key is never used as somebody else).

| Variable | Default | What it does |
|---|---|---|
| `AUDIOBOOKSHELF_URL` | – | Audiobookshelf's address as this container reaches it (`http://audiobookshelf:80`). Without it, each person types an address under Settings. |
| `AUDIOBOOKSHELF_PUBLIC_URL` | – | What a browser opens for "Open in Audiobookshelf" |
| `AUDIOBOOKSHELF_TOKEN` | – | The owner's account, copied once into their connection: an API key of that account. Progress is written for the account the key belongs to. |
| `AUDIOBOOKSHELF_USER` + `AUDIOBOOKSHELF_PASSWORD` | – | Or the owner's user name and password (signed in once; only the refresh token is kept). Needs local sign-in on in Audiobookshelf. |
| `SYNC_EVERY` | `5m` | How often each person's listening sessions are copied from Audiobookshelf (every app's, this one's included) |

## Personal integrations

| Variable | Default | What it does |
|---|---|---|
| `NOTIFY_URL` / `NOTIFY_FORMAT` | – / guessed | The owner's Apprise `…/notify/<key>`, ntfy topic URL or JSON webhook: finished books, the weekly recap. Everyone sets their own under Settings → Notifications. |

## Pocket Core

| Variable | Default | What it does |
|---|---|---|
| `CORE_URL` / `CORE_REGISTRATION_KEY` | – | [Pocket Core](https://github.com/abjelosevic88/pocket-homelab-core): register on start (the key is needed once; the app token is kept in `$DATA_DIR/core.token`), "Sign in with Pocket Core", accept the tokens Core gives paired phones, get each person's Audiobookshelf login from Core, publish `audiobooks.finished`, offer `listening:read` to other Pocket apps (Pocket Day, Pocket Bookshelf) |
| `CORE_APP_URL` | `BASE_URL` | How Core reaches this app for health checks (e.g. `http://host.docker.internal:8202`) |

With `CORE_URL` empty the app behaves exactly as a standalone app: every Core-only choice stays visible, greyed out
with a note.
