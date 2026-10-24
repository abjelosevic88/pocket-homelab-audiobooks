#!/usr/bin/env python3
"""Exercise native-player routes against the throwaway demo ABS, never production.

ABS_DEMO_URL=http://127.0.0.1:13399 ABS_DEMO_KEY_FILE=.../ana.key \
  POCKET_BINARY=/tmp/pocket-audiobooks python3 scripts/gateway-smoke.py
"""
import json
import os
import pathlib
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request

abs_base = os.environ['ABS_DEMO_URL'].rstrip('/')
assert urllib.parse.urlparse(abs_base).hostname in ('127.0.0.1', 'localhost'), 'demo must be local'
assert urllib.parse.urlparse(abs_base).port == 13399, 'use the isolated demo on port 13399'
abs_key = pathlib.Path(os.environ['ABS_DEMO_KEY_FILE']).read_text().strip()
binary = os.environ['POCKET_BINARY']


def request(base, path, token='', method='GET', data=None, headers=None, raw=False):
    h = dict(headers or {})
    if token:
        h['Authorization'] = 'Bearer ' + token
    body = None if data is None else json.dumps(data).encode()
    if body is not None:
        h['Content-Type'] = 'application/json'
    req = urllib.request.Request(base + path, data=body, method=method, headers=h)
    with urllib.request.urlopen(req, timeout=45) as res:
        body = res.read()
        return (res.status, dict(res.headers), body) if raw else (json.loads(body) if 'json' in res.headers.get('Content-Type', '') else body.decode()) if body else None


with tempfile.TemporaryDirectory(prefix='pab-gateway-') as tmp:
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    base = f'http://127.0.0.1:{port}'
    password = secrets.token_hex(24)
    env = dict(os.environ, DATA_DIR=tmp, HOST='127.0.0.1', PORT=str(port), BASE_URL=base,
               AUTH_MODE='local', SECURE_COOKIES='false', ADMIN_EMAIL='ana@example.com',
               ADMIN_PASSWORD=password, AUDIOBOOKSHELF_URL=abs_base, AUDIOBOOKSHELF_TOKEN=abs_key,
               CORE_URL='', NOTIFY_URL='', LOG_LEVEL='error')
    with open(tmp + '/server.log', 'w+') as log:
        app = subprocess.Popen([binary], env=env, stdout=log, stderr=log)
        try:
            for _ in range(100):
                try:
                    request(base, '/healthz')
                    break
                except Exception:
                    if app.poll() is not None:
                        raise RuntimeError('test app failed to start')
                    time.sleep(.1)
            else:
                raise RuntimeError('test app did not become healthy')
            # Obtain a normal personal token via the web API, then use only
            # the same native protocol the Pocket Homelab client speaks.
            jar = __import__('http.cookiejar', fromlist=['CookieJar']).CookieJar()
            opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
            def web(path, data):
                req = urllib.request.Request(base + '/api/v1' + path, data=json.dumps(data).encode(), headers={'Content-Type':'application/json'})
                with opener.open(req) as res:
                    return json.load(res)
            web('/auth/login', {'email':'ana@example.com','password':password})
            token = web('/tokens', {'name':'native smoke'})['token']
            status = request(base, '/status')
            assert status['app'] == 'audiobookshelf'
            me = request(base, '/api/me', token)
            assert me['token'] == token and 'refreshToken' not in me
            libs = request(base, '/api/libraries', token)['libraries']
            lib = next(x for x in libs if x['mediaType'] == 'book')
            for suffix in ['stats','filterdata','series','authors','personalized']:
                request(base, '/api/libraries/' + lib['id'] + '/' + suffix, token)
            items = request(base, '/api/libraries/' + lib['id'] + '/items?limit=30', token)['results']
            item = next(x for x in items if x['media']['metadata']['title'] == 'Dracula')
            item_id = item['id']
            expanded = request(base, '/api/items/' + item_id + '?expanded=1&include=progress', token)
            assert expanded['media']['chapters']
            for suffix in ['listening-stats','items-in-progress']:
                request(base, '/api/me/' + suffix, token)
            cover = request(base, '/api/items/' + item_id + '/cover?token=' + token, raw=True)
            assert cover[0] == 200 and len(cover[2]) > 100
            for transcode in [False, True]:
                request(base, '/api/me/progress/' + item_id, token, 'PATCH', {'currentTime':0,'isFinished':False})
                session = request(base, '/api/items/' + item_id + '/play', token, 'POST', {
                    'forceDirectPlay':not transcode, 'forceTranscode':transcode,
                    'supportedMimeTypes':['audio/mp4','audio/mpeg'], 'mediaPlayer':'pocket-gateway-smoke',
                    'deviceInfo':{'clientName':'Pocket Homelab gateway smoke','deviceId':'demo-gateway-smoke'}})
                sid = session['id']
                try:
                    track = session['audioTracks'][0]['contentUrl']
                    track += ('&' if '?' in track else '?') + 'token=' + token
                    # ABS may take a moment to produce the first HLS segment.
                    for attempt in range(30):
                        try:
                            media = request(base, track, headers={} if transcode else {'Range':'bytes=0-1023'}, raw=True)
                            break
                        except urllib.error.HTTPError as err:
                            if err.code not in (404, 503) or attempt == 29: raise
                            time.sleep(.5)
                    if transcode:
                        playlist = media[2].decode()
                        segment = next(line for line in playlist.splitlines() if line and not line.startswith('#'))
                        assert 'token=' + token in segment
                        segment_path = urllib.parse.urlsplit(urllib.parse.urljoin(base + track, segment))
                        for attempt in range(30):
                            try:
                                result = request(base, segment_path.path + '?' + segment_path.query, raw=True)
                                break
                            except urllib.error.HTTPError as err:
                                if err.code != 404 or attempt == 29: raise
                                time.sleep(.5)
                        assert result[0] == 200 and len(result[2]) > 0
                    else:
                        assert media[0] == 206 and len(media[2]) == 1024
                    request(base, '/api/session/' + sid + '/sync', token, 'POST', {'currentTime':1234,'timeListened':15,'duration':session['duration']})
                finally:
                    request(base, '/api/session/' + sid + '/close', token, 'POST', {'currentTime':1234,'timeListened':0,'duration':session['duration']})
                print('PASS:', 'HLS playlist + segment' if transcode else 'direct byte-range audio', 'and progress sync')
            progress = request(base, '/api/me/progress/' + item_id, token)
            assert abs(progress['currentTime'] - 1234) < 1
            direct_progress = request(abs_base, '/api/me/progress/' + item_id, abs_key)
            assert abs(direct_progress['currentTime'] - 1234) < 1
            resumed = request(base, '/api/items/' + item_id + '/play', token, 'POST', {'forceDirectPlay':True})
            try:
                assert abs(resumed['currentTime'] - 1234) < 1
            finally:
                request(base, '/api/session/' + resumed['id'] + '/close', token, 'POST', {})
            bookmark_path = '/api/me/item/' + item_id + '/bookmark'
            request(base, bookmark_path, token, 'POST', {'time':1234,'title':'Gateway smoke'})
            request(base, bookmark_path + '/1234', token, 'DELETE')
            print('PASS: library, covers, chapters, listening stats, bookmarks and resume from saved ABS position')
        finally:
            app.terminate()
            try:
                app.wait(timeout=10)
            except subprocess.TimeoutExpired:
                app.kill()
                app.wait()
