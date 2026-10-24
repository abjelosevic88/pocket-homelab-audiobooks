# Demo library

`make.sh` runs a throwaway Audiobookshelf (`ghcr.io/advplyr/audiobookshelf`, port 13399, data in
`~/.cache/pocket-audiobooks-dev`) with the 22 public-domain books in [titles.txt](titles.txt), for the README
screenshots and the browser test in `scripts/screenshots.mjs`. The audio is generated with Audiobookshelf's own
ffmpeg — half an hour of silence encoded once (Opus, 6 kb/s) and copied to each book's real length, with chapters;
*The Adventures of Sherlock Holmes* comes as twelve files, one per story — about 220 MB in all. `seed.py` then sets
up the server: a library matched on Audible (covers, descriptions, narrators of real editions), the listener `ana`
with her own API key, and six weeks of her listening on two phones and the web.

```sh
scripts/demo-library/make.sh          # build (idempotent), start, seed; ana's API key ends up in ~/.cache/pocket-audiobooks-dev/ana.key
scripts/demo-library/make.sh stop     # remove the container (the files stay)
```

The books themselves are in the public domain; the covers Audiobookshelf finds belong to their publishers and are
only fetched into the throwaway server.
