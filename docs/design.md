# How it works

## Why on top of Audiobookshelf, not instead of it

An audiobook server is mostly plumbing Audiobookshelf already does well: scanning folders into books (one file or
fifty), chapters, matching metadata and covers on Audible and Google, transcoding what a client can't play, and the
API the phone apps (Audiobookshelf, AudioBooth, Plappa…) speak, with offline downloads and CarPlay. Rewriting that
would take months and cut off every phone in the house.

What its own web client doesn't do as well is the listening around the books: a calm player that keeps your place
in step with the phone, a history you own with stats and streaks, a queue of what's next, and a place in the rest of
the Pocket apps. That is Pocket Audiobooks:

| Audiobookshelf | Pocket Audiobooks |
|---|---|
| Files, scanning, chapters, metadata, covers | A fast, calm web app for the household, desktop and phone |
| Each account's place, finished marks, bookmarks | A player that plays the files as they are (or HLS when the browser can't) and writes your place back every 15 seconds |
| Listening sessions from every app | A listening history it owns, with stats, streaks, a daily goal and a day view |
| The API every phone app speaks | Up next, series and authors pages, "continue on the phone / continue here" |
| | Pocket Core: one sign-in, per-person logins, Pocket Day and Pocket Bookshelf through app links |

The split mirrors Pocket Watch on Jellyfin, Pocket Music on Navidrome and Pocket Photos on Immich. Keep using the
phone apps: they share your place, finished books and bookmarks with Pocket Audiobooks because both act as the
same Audiobookshelf account.

## Per person, always

Everyone signs in to Pocket Audiobooks with their own account and listens with **their own** Audiobookshelf
account: an API key made for them, a refresh token from signing in once (the password itself is never kept), or the
login Pocket Core holds for them. A key Pocket Core hands out is checked against the person's Audiobookshelf user id,
so a box-wide admin key is never used as somebody else. History, finished books, Up next and settings are keyed by
person; another person's ids answer 404 and admins get no read access to anyone's listening.

## Playing

The browser asks `POST /play/{book}` with the audio types it can decode. Pocket Audiobooks opens an Audiobookshelf
**playback session** as the person — exactly what the phone apps do — and answers with the book's files laid out on
one timeline (`start` + `duration` each), the chapters and the person's place. If every file fits the browser it
plays them **as they are** (byte ranges, nothing for the server to do), one after the other; otherwise
Audiobookshelf makes one HLS stream, played with hls.js (or natively in Safari). If a browser claims a type and then
can't decode it, the player asks again with `force_transcode` and remembers the type.

The browser never sees an Audiobookshelf key: every file, playlist and segment goes through
`/api/v1/media/…`, where the person's key is added on the way.

Every 15 seconds while playing, and on pause, the player reports where it is and how many seconds it played
(`/sessions/{id}/sync`). Audiobookshelf moves the person's place and keeps the session, so the phone picks up where
the web stopped. Closing the tab sends a last report (`sendBeacon`). If Audiobookshelf restarted and forgot the
session, the place is written directly and a new session takes over.

Around the player: chapters (previous/next on the lock screen and headphone buttons), speed per book, skip back and
forward, a few seconds back after a long pause, a sleep timer (minutes or the end of the chapter), bookmarks (kept in
Audiobookshelf), and at the end of a book the next one — from Up next, else the next in its series.

**Playing elsewhere.** `GET /now-playing` looks at the person's newest Audiobookshelf listening session (and at
other browsers on this app): when the phone is playing and this browser isn't, the bar shows "Playing on Ana's
iPhone" with a ticking clock and **Continue here**, which opens the book at that moment.

## History

Audiobookshelf saves a listening session for every stretch someone listens, in any app. Every `SYNC_EVERY` (and
soon after this app's player reports) the sessions that changed since the last look are copied here; the first look
copies them all. One row per session: the book (copied, so it survives the file leaving the library), when, how many
seconds, from where to where, on which device. Finished books come from the person's finished marks.

Stats, the day view (`/day/{date}`, which Pocket Day can read through an app link), the weekly recap, streaks and
the `audiobooks.finished` event all come from these rows.
