# Behind a reverse proxy

Pocket Audiobooks is one HTTP service on port 8080. Audio is long: a book plays as one request with byte ranges
per file for as long as it plays, a converted one is an HLS playlist and a stream of segments
(`/api/v1/media/…`). So the proxy must not buffer responses and must not time them out early. Set
`SECURE_COOKIES=true` behind HTTPS and `BASE_URL` to the public address.

## Caddy

```caddy
audio.example.com {
	reverse_proxy pocket-audiobooks:8080 {
		flush_interval -1
	}
}
```

If iPhones stall on playback behind Caddy, switch HTTP/3 off for the site (`protocols h1 h2`): Safari's media
stack and QUIC don't always get along.

## nginx

```nginx
location / {
    proxy_pass http://pocket-audiobooks:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_request_buffering off;
    proxy_read_timeout 4h;
}
```

## Traefik

Nothing special: Traefik streams by default.

## Audiobookshelf

Pocket Audiobooks talks to Audiobookshelf over the internal network (`AUDIOBOOKSHELF_URL`) and passes audio
through itself, so Audiobookshelf doesn't need to be public for Pocket Audiobooks. Keep it reachable for the phone
apps as before — Pocket Audiobooks doesn't take its hostname.
