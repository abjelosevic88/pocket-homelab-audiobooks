<p align="center"><img src="web/public/favicon.svg" width="72" alt=""></p>
<h1 align="center">Pocket Audiobooks</h1>
<p align="center">Audiobooks for the whole household, on top of Audiobookshelf: a calm web player that keeps your place in step with the phone apps (continue on the phone, continue here), chapters, bookmarks, speed and a sleep timer, series and authors, Up next, and a listening history it keeps from every Audiobookshelf app, with stats, streaks and a daily goal.<br>One Go binary, one SQLite file, one volume.</p>
<p align="center">
  <a href="https://github.com/abjelosevic88/pocket-homelab-audiobooks/actions"><img src="https://github.com/abjelosevic88/pocket-homelab-audiobooks/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/abjelosevic88/pocket-homelab-audiobooks/pkgs/container/pocket-homelab-audiobooks"><img src="https://img.shields.io/badge/ghcr.io-amd64%20%7C%20arm64%20%7C%20armv7-blue" alt="image"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="MIT"></a>
</p>

---

<p align="center"><img src="docs/screenshots/desktop-home.png" width="900" alt="Home: pick up where you left off, continue listening, up next"></p>

<table>
  <tr>
    <td width="50%"><img src="docs/screenshots/desktop-book.png" alt="A book with chapters, bookmarks and your listening"></td>
    <td width="50%"><img src="docs/screenshots/desktop-player.png" alt="The player"></td>
  </tr>
  <tr>
    <td align="center"><sub>A book: your place, chapters to jump to, bookmarks, the rest of its series, how long you've listened.</sub></td>
    <td align="center"><sub>The player: chapters, speed, sleep timer (minutes or the end of the chapter), bookmarks; your place goes back to Audiobookshelf every 15 s.</sub></td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/screenshots/desktop-library.png" alt="The library"></td>
    <td width="50%"><img src="docs/screenshots/desktop-series.png" alt="Series"></td>
  </tr>
  <tr>
    <td align="center"><sub>The library: in progress, not started, finished; by title, author, length, last listened.</sub></td>
    <td align="center"><sub>Series, with how far you are in each.</sub></td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/screenshots/desktop-stats.png" alt="Listening stats"></td>
    <td width="50%"><img src="docs/screenshots/desktop-history.png" alt="History by day"></td>
  </tr>
  <tr>
    <td align="center"><sub>Stats from your history — here and in every Audiobookshelf app: time, finished books, streaks.</sub></td>
    <td align="center"><sub>History, every listening session by day, with the device it played on.</sub></td>
  </tr>
</table>

<p align="center">
  <img src="docs/screenshots/phone-home.png" width="240" alt="Home on a phone">&nbsp;&nbsp;
  <img src="docs/screenshots/phone-book.png" width="240" alt="A book on a phone">&nbsp;&nbsp;
  <img src="docs/screenshots/desktop-book-dark.png" width="420" alt="A book in dark mode">
</p>

## Connect Pocket Homelab

Use this app's base URL in Pocket Homelab's **Audiobookshelf** settings and a personal token from
**Settings → API & shortcuts**. The existing native player can browse, play, download, bookmark and save progress
through Pocket Audiobooks. Audiobookshelf continues to store the library and listening position underneath.

With Pocket Core, select **Pocket Audiobooks** under **Devices → Apps on your devices → Audiobooks** and refresh
the app's Core configuration. Core supplies the gateway URL and the device's own token using the settings the
existing app already understands; no phone app rebuild is required.


## Why on top of Audiobookshelf

Audiobookshelf already does the server part very well — scanning folders into books, chapters, Audible and Google
metadata and covers, transcoding, and the API the phone apps speak (offline downloads, CarPlay). Pocket Audiobooks
doesn't replace any of that: it plays your library *through* Audiobookshelf, with each person's own account, and
adds what the listening around the books needs — a player that keeps your place in step with the phone, a history
you own, Up next, stats and streaks, and a place among the other Pocket apps. Keep the phone apps: your place,
finished books and bookmarks are shared. More in [docs/design.md](docs/design.md).

## What it does

**Listening**
- Home: pick up where you left off, everything in progress, Up next, the next book of series you started, recently
  added, books you haven't started, listen again.
- A player that stays at the bottom while you browse: the files as they are when the browser can play them (books
  in one file or in fifty), HLS through Audiobookshelf when it can't. Chapters, speed per book, skip back/forward,
  a few seconds back after a long pause, a sleep timer, bookmarks, lock-screen and headphone controls, keyboard
  shortcuts. When a book ends, the next one from Up next or its series.
- Your place goes back to Audiobookshelf every 15 seconds and on pause, as a real playback session: the phone picks
  up where the web stopped. When the phone plays, the web shows it — **Continue here** takes over at that moment.
- Library by library: books (filters, sorting, genres, narrators), series, authors, search across everything (⌘K).

**Yours**
- A listening history Pocket Audiobooks keeps: every session in every Audiobookshelf app, copied every few minutes,
  back to the first one. Stats: time listened, finished books, top books, authors, narrators, genres, devices,
  weekdays and hours, streaks and an optional daily goal. A push when you finish a book, a weekly recap.
- Mark finished or not, start over, take a book off Continue listening — written to Audiobookshelf.

**The Pocket apps**
- With [Pocket Core](https://github.com/abjelosevic88/pocket-homelab-core): one sign-in, each person's
  Audiobookshelf login from Core, Pocket Day and Pocket Bookshelf can read what you listened to
  (`listening:read`), and an `audiobooks.finished` event.
- Strictly per person: history, Up next and settings are yours; admins don't see anyone else's listening.

## Run it

```sh
docker run -d --name pocket-audiobooks -p 8080:8080 -v ./data:/data \
  -e BASE_URL=https://audio.example.com -e SECURE_COOKIES=true \
  -e AUDIOBOOKSHELF_URL=http://audiobookshelf:80 \
  ghcr.io/abjelosevic88/pocket-homelab-audiobooks:latest
```

Open it, create the first account, then connect Audiobookshelf under Settings → Audiobookshelf: an API key made for
your account (Audiobookshelf → Settings → API Keys), or your password. See
[docs/configuration.md](docs/configuration.md) for every option, [docs/api.md](docs/api.md) for the API (and a
Homepage widget), [docs/reverse-proxy.md](docs/reverse-proxy.md) and [docs/backup.md](docs/backup.md).

Try it with a demo library: [scripts/demo-library/make.sh](scripts/demo-library/make.sh) runs a throwaway
Audiobookshelf with 22 public-domain classics at their real lengths (generated silent audio with chapters; covers
and descriptions matched on Audible by Audiobookshelf) and six weeks of a demo listener's history. The screenshots
above are that library; [scripts/screenshots.mjs](scripts/screenshots.mjs) is also the browser test.

## Built with

Go, chi, SQLite (modernc), React 19, Vite, hls.js. Works with Audiobookshelf 2.26+ (API keys; earlier versions with a
password); works alone, and with [Pocket Core](https://github.com/abjelosevic88/pocket-homelab-core) for single
sign-on, paired phones and the links between apps. MIT licensed.
