#!/usr/bin/env bash
# A throwaway Audiobookshelf with a demo library, for the README screenshots
# and the end-to-end test: 22 public-domain books by their real authors, in
# their real series, at their real lengths, with chapters. The audio is
# generated (silence, Opus in .m4b; one book as twelve .m4a files) with
# Audiobookshelf's own ffmpeg, so the library is ~300 MB; Audiobookshelf matches
# covers and descriptions on Audible itself (seed.py).
#
#   scripts/demo-library/make.sh            # build (idempotent), start on :13399, seed
#   scripts/demo-library/make.sh stop       # remove the container (files stay)
#
# Accounts: root / pocket-demo (admin), ana / pocket-demo (the demo listener).
set -euo pipefail
DIR=${DEMO_DIR:-$HOME/.cache/pocket-audiobooks-dev}
PORT=${DEMO_PORT:-13399}
NAME=pab-demo-abs
IMAGE=${DEMO_IMAGE:-ghcr.io/advplyr/audiobookshelf:latest}
HERE=$(cd "$(dirname "$0")" && pwd)

if [ "${1:-}" = stop ]; then docker rm -f "$NAME"; exit 0; fi
mkdir -p "$DIR"/{config,metadata,books}
if ! docker ps --format '{{.Names}}' | grep -qx "$NAME"; then
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker run -d --name "$NAME" -p "$PORT:80" -v "$DIR/config:/config" -v "$DIR/metadata:/metadata" -v "$DIR/books:/audiobooks" -e TZ=Europe/Sarajevo "$IMAGE" >/dev/null
fi

ff() { docker exec "$NAME" ffmpeg -nostdin -loglevel error -y "$@"; }
# half an hour of silence, encoded once (Opus at 6 kb/s), then copied to each book's length
docker exec "$NAME" test -s /tmp/base.m4b || ff -f lavfi -i "anullsrc=r=48000:cl=mono" -t 1800 -c:a libopus -b:a 6k -vbr on -application voip -frame_duration 60 -f mp4 /tmp/base.m4b
concat() { # base seconds: a concat list long enough
  docker exec "$NAME" sh -c "rm -f /tmp/list.txt; n=\$(( $2 / 1800 + 1 )); i=0; while [ \$i -lt \$n ]; do echo \"file '$1'\" >> /tmp/list.txt; i=\$((i+1)); done"
}
# chapters as an ffmetadata file: n chapters over the length
chapters() { # seconds n title author album year
  local sec=$1 n=$2
  printf ';FFMETADATA1\ntitle=%s\nartist=%s\nalbum_artist=%s\nalbum=%s\ndate=%s\ngenre=Audiobook\n' "$3" "$4" "$4" "$5" "$6"
  local step=$((sec * 1000 / n))
  for i in $(seq 1 "$n"); do
    printf '[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=Chapter %d\n' $(((i - 1) * step)) $((i == n ? sec * 1000 : i * step)) "$i"
  done
}
STORIES=("A Scandal in Bohemia" "The Red-Headed League" "A Case of Identity" "The Boscombe Valley Mystery" "The Five Orange Pips" "The Man with the Twisted Lip" "The Adventure of the Blue Carbuncle" "The Adventure of the Speckled Band" "The Adventure of the Engineer's Thumb" "The Adventure of the Noble Bachelor" "The Adventure of the Beryl Coronet" "The Adventure of the Copper Beeches")
grep -v '^#' "$HERE/titles.txt" | while IFS='|' read -r author series seq title hours chaps year; do
  sec=$(awk "BEGIN{print int($hours*3600)}")
  if [ -n "$series" ]; then dir="/audiobooks/$author/$series/Vol $seq - $title"; else dir="/audiobooks/$author/$title"; fi
  docker exec "$NAME" mkdir -p "$dir"
  if [ "$chaps" = files ]; then
    per=$((sec / ${#STORIES[@]}))
    for i in "${!STORIES[@]}"; do
      n=$(printf '%02d' $((i + 1)))
      out="$dir/$n - ${STORIES[$i]}.m4a"
      docker exec "$NAME" test -s "$out" && continue
      concat /tmp/base.m4b "$per" > /dev/null
      ff -f concat -safe 0 -i /tmp/list.txt -t "$per" -c copy \
        -metadata title="${STORIES[$i]}" -metadata artist="$author" -metadata album="$title" -metadata track="$((i + 1))" -metadata date="$year" -f mp4 "$out"
    done
  else
    out="$dir/$title.m4b"
    docker exec "$NAME" test -s "$out" && continue
    chapters "$sec" "$chaps" "$title" "$author" "$title" "$year" | docker exec -i "$NAME" sh -c 'cat > /tmp/meta.txt'
    concat /tmp/base.m4b "$sec" > /dev/null
    ff -f concat -safe 0 -i /tmp/list.txt -i /tmp/meta.txt -map 0:a -map_metadata 1 -map_chapters 1 -t "$sec" -c copy -f mp4 -movflags +faststart "$out"
  fi
  echo "made: $title"
done
du -sh "$DIR/books"
for i in $(seq 1 60); do curl -fs "http://localhost:$PORT/status" >/dev/null && break; sleep 2; done
python3 -I "$HERE/seed.py" "http://localhost:$PORT"
