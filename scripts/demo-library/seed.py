#!/usr/bin/env python3
"""Seed the demo Audiobookshelf (make.sh runs this): the root account, the
"Audiobooks" library (scanned, then matched on Audible for covers and
descriptions), the demo listener "ana" with her own API key, and six weeks of
her listening on two phones and the web, a few books finished, one in the
middle of a chapter.

    python3 seed.py http://localhost:13399

Idempotent: a second run only fills what is missing. Writes ana's API key to
$DEMO_DIR/ana.key (default ~/.cache/pocket-audiobooks-dev).
"""
import json, os, random, sys, time, uuid
import urllib.request, urllib.error
from datetime import datetime, timedelta

BASE = sys.argv[1].rstrip('/') if len(sys.argv) > 1 else 'http://localhost:13399'
DIR = os.environ.get('DEMO_DIR', os.path.expanduser('~/.cache/pocket-audiobooks-dev'))
PW = 'pocket-demo'


def call(method, path, body=None, token=None, raw=False):
    req = urllib.request.Request(BASE + path, method=method, data=json.dumps(body).encode() if body is not None else None)
    req.add_header('Content-Type', 'application/json')
    if token:
        req.add_header('Authorization', 'Bearer ' + token)
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            data = r.read()
            if raw:
                return data
            return json.loads(data) if data and data[:1] in (b'{', b'[') else None
    except urllib.error.HTTPError as e:
        raise SystemExit(f'{method} {path}: HTTP {e.code} {e.read()[:300]!r}')


def login(user):
    return call('POST', '/login', {'username': user, 'password': PW})['user']['accessToken']


status = call('GET', '/status')
if not status.get('isInit'):
    call('POST', '/init', {'newRoot': {'username': 'root', 'password': PW}})
    print('initialised: root /', PW)
root = login('root')

libs = call('GET', '/api/libraries', token=root)['libraries']
lib = next((l for l in libs if l['name'] == 'Audiobooks'), None)
if not lib:
    lib = call('POST', '/api/libraries', {'name': 'Audiobooks', 'folders': [{'fullPath': '/audiobooks'}], 'icon': 'audiobookshelf',
                                          'mediaType': 'book', 'provider': 'audible'}, token=root)
    print('library created')
lid = lib['id']


def items():
    return call('GET', f'/api/libraries/{lid}/items?limit=200&minified=1', token=root)['results']


want = sum(1 for line in open(os.path.join(os.path.dirname(__file__), 'titles.txt')) if line.strip() and not line.startswith('#'))
if len(items()) < want:
    call('POST', f'/api/libraries/{lid}/scan', token=root)
    for _ in range(90):
        time.sleep(2)
        if len(items()) >= want:
            break
    time.sleep(3)
books = items()
print(f'{len(books)} books in the library')

# covers and descriptions from Audible (titles, authors and series stay ours)
for b in books:
    m = b['media']
    if m.get('coverPath'):
        continue
    md = m['metadata']
    for provider in ('audible', 'google'):
        try:
            r = call('POST', f'/api/items/{b["id"]}/match', {'provider': provider, 'title': md['title'], 'author': md.get('authorName') or '',
                                                            'overrideCover': True, 'overrideDetails': False}, token=root)
        except SystemExit as e:
            print('  match failed:', md['title'], e)
            continue
        if r and r.get('updated'):
            print('  matched on', provider + ':', md['title'])
            break
books = items()
# a match may add the publisher's series ("BBC Children's Classics"): keep only the real ones
series_of = {}
for line in open(os.path.join(os.path.dirname(__file__), 'titles.txt')):
    if line.strip() and not line.startswith('#'):
        a, s, q, t = line.split('|')[:4]
        series_of[t] = (s, q)
for b in books:
    md = b['media']['metadata']
    want_s, want_q = series_of.get(md['title'], ('', ''))
    full = call('GET', f'/api/items/{b["id"]}?expanded=1', token=root)
    have = full['media']['metadata'].get('series') or []
    keep = [x for x in have if x['name'] == want_s]
    if want_s and not keep:
        keep = [{'name': want_s, 'sequence': want_q}]
    if [x['name'] for x in have] != [x['name'] for x in keep]:
        call('PATCH', f'/api/items/{b["id"]}/media', {'metadata': {'series': [{'name': x['name'], 'sequence': x.get('sequence') or want_q} for x in keep]}}, token=root)
        print('  series fixed:', md['title'], [x['name'] for x in have], '→', [x['name'] for x in keep])
books = items()

# the demo listener and her own API key
users = call('GET', '/api/users', token=root)['users']
ana = next((u for u in users if u['username'] == 'ana'), None)
if not ana:
    ana = call('POST', '/api/users', {'username': 'ana', 'password': PW, 'type': 'user', 'isActive': True, 'email': 'ana@example.com',
                                      'permissions': {'download': True, 'update': False, 'delete': False, 'upload': False, 'accessAllLibraries': True, 'accessAllTags': True}}, token=root)['user']
    print('user ana created')
keyfile = os.path.join(DIR, 'ana.key')
if not os.path.exists(keyfile):
    k = call('POST', '/api/api-keys', {'name': 'Pocket Audiobooks demo', 'userId': ana['id'], 'isActive': True}, token=root)['apiKey']['apiKey']
    os.makedirs(DIR, exist_ok=True)
    with open(keyfile, 'w') as f:
        f.write(k)
    print('API key for ana written to', keyfile)
key = open(keyfile).read().strip()

# six weeks of listening
me = call('GET', '/api/me', token=key)
if not any(p.get('mediaItemType') == 'book' for p in me.get('mediaProgress', [])):
    by = {b['media']['metadata']['title']: b for b in books}
    random.seed(7)
    devices = [{'clientName': 'AudioBooth iOS', 'deviceName': "Ana's iPhone", 'deviceId': 'demo-iphone', 'osName': 'iOS'},
               {'clientName': 'Abs Android', 'deviceName': 'Pixel 8', 'deviceId': 'demo-pixel', 'manufacturer': 'Google', 'model': 'Pixel 8'},
               {'clientName': 'Pocket Audiobooks', 'deviceName': 'Firefox on macOS', 'deviceId': 'demo-web'}]
    plan = [  # (title, days ago it started, finish?)
        ('A Study in Scarlet', 41, True), ('The Sign of the Four', 33, True), ('The Time Machine', 27, True),
        ('Frankenstein', 22, True), ('The Wonderful Wizard of Oz', 12, True), ('Dracula', 9, False),
        ('The Adventures of Sherlock Holmes', 6, False), ('Treasure Island', 3, False),
    ]
    now = datetime.now()
    for title, ago, finish in plan:
        b = by.get(title)
        if not b:
            continue
        dur = b['media']['duration']
        pos = 0.0
        day = ago
        target = dur if finish else dur * random.uniform(.25, .62)
        while pos < target and day >= 0:
            if random.random() < .82:
                for _ in range(random.choice((1, 1, 2))):
                    hour = random.choice((7, 8, 8, 12, 17, 18, 21, 22))
                    start = (now - timedelta(days=day)).replace(hour=hour, minute=random.randint(0, 50), second=0, microsecond=0)
                    if start > now:
                        continue
                    listen = min(target - pos, random.uniform(18, 75) * 60)
                    speed = 1.2 if 'Sherlock' in title or 'Scarlet' in title or 'Sign' in title else 1.0
                    dev = random.choice(devices[:2]) if random.random() < .75 else devices[2]
                    started, ended = int(start.timestamp() * 1000), int((start + timedelta(seconds=listen / speed)).timestamp() * 1000)
                    call('POST', '/api/session/local', {
                        'id': str(uuid.uuid4()), 'libraryItemId': b['id'], 'mediaType': 'book', 'displayTitle': title,
                        'displayAuthor': b['media']['metadata'].get('authorName') or '', 'duration': dur, 'playMethod': 3,
                        'mediaPlayer': dev['clientName'], 'deviceInfo': dev, 'date': start.strftime('%Y-%m-%d'), 'dayOfWeek': start.strftime('%A'),
                        'timeListening': listen / speed, 'startTime': pos, 'currentTime': pos + listen, 'startedAt': started, 'updatedAt': ended,
                    }, token=key)
                    pos += listen
            day -= 1
        if finish:
            call('PATCH', f'/api/me/progress/{b["id"]}', {'isFinished': True, 'duration': dur, 'currentTime': dur,
                                                          'finishedAt': int((now - timedelta(days=max(0, day))).timestamp() * 1000)}, token=key)
        print(f'  history: {title} ({"finished" if finish else f"{pos / dur:.0%}"})')
    # a bookmark or two
    b = by.get('Dracula')
    if b:
        call('POST', f'/api/me/item/{b["id"]}/bookmark', {'time': 3725, 'title': "Jonathan's journal: the castle door"}, token=key)
        call('POST', f'/api/me/item/{b["id"]}/bookmark', {'time': 18044, 'title': 'Lucy, Mina and the storm at Whitby'}, token=key)
print('demo Audiobookshelf ready on', BASE, '— ana /', PW, '— API key in', keyfile)
