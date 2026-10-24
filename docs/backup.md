# Backup

Everything Pocket Audiobooks keeps is in `DATA_DIR` (`/data` in the container):

| File | What it is |
|---|---|
| `pocket-audiobooks.db` (+ `-wal`, `-shm`) | Listening history, finished books, Up next, settings, accounts |
| `secret.key` | Seals the Audiobookshelf keys and refresh tokens people connected with. Lose it and they connect again; nothing else is lost. |
| `core.token`, `core.token.oidc.json` | The app's Pocket Core registration (re-created by registering again) |
| `backups/` | Snapshots made by `pocket-audiobooks backup` or Settings → Backup & data |

The books, each person's place, finished marks and bookmarks live in Audiobookshelf — back that up there. The
listening history can always be read again from Audiobookshelf (Settings → Audiobookshelf → Read everything again),
as long as Audiobookshelf still has its sessions.

**Never copy the live database file.** Use one of:

```sh
docker exec pocket-audiobooks pocket-audiobooks backup            # writes data/backups/pocket-audiobooks-<time>.db
curl -H "Authorization: Bearer pab_…" https://audio.example.com/api/v1/backup -o audiobooks.db   # admins
```

Each person can export their own history, finished books and Up next as JSON under Settings → Backup & data, and
import it on another box.

To restore: stop the app, replace `pocket-audiobooks.db` with a snapshot (delete stale `-wal`/`-shm`), start it.
