#!/usr/bin/env python3
"""Read-only Audiobookshelf migration audit. Reports never contain user credentials.

SQLite snapshots are sensitive: they contain login material and personal data.
Snapshots are written with mode 0600 in a new mode-0700 directory. Media is not
copied or changed. Keep the source online until a verified cutover is ready.
"""
import argparse
import datetime as dt
import json
import os
from pathlib import Path
import sqlite3
import sys


def quote(name):
    return '"' + name.replace('"', '""') + '"'


def connect(path):
    return sqlite3.connect(Path(path).resolve().as_uri() + '?mode=ro', uri=True)


def rows(db, table):
    cur = db.execute('SELECT * FROM ' + quote(table))
    return [dict(zip([c[0] for c in cur.description], row)) for row in cur]


def decoded(value, default):
    if value is None or value == '':
        return default
    if isinstance(value, (dict, list)):
        return value
    return json.loads(value)


def mapped(path, mounts):
    for container, host in sorted(mounts.items(), key=lambda x: -len(x[0])):
        try:
            suffix = Path(path).relative_to(container)
        except ValueError:
            continue
        root = Path(host).resolve()
        target = (root / suffix).resolve()
        if not target.is_relative_to(root):
            return None
        return target
    return None


def audit(db, mounts):
    tables = sorted(r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"))
    counts = {name: db.execute('SELECT count(*) FROM ' + quote(name)).fetchone()[0] for name in tables}
    required = {'libraries', 'libraryFolders', 'libraryItems', 'books', 'users', 'mediaProgresses', 'playbackSessions'}
    missing = sorted(required - set(tables))
    report = {'format': 1, 'created_at': dt.datetime.now(dt.timezone.utc).isoformat(),
              'integrity': [r[0] for r in db.execute('PRAGMA quick_check')], 'counts': counts,
              'missing_required_tables': missing, 'libraries': [], 'files': {}, 'user_state': {}}
    if missing:
        return report
    folders = rows(db, 'libraryFolders')
    items = rows(db, 'libraryItems')
    for lib in rows(db, 'libraries'):
        report['libraries'].append({'id': lib['id'], 'name': lib['name'], 'media_type': lib['mediaType'],
            'items': sum(x['libraryId'] == lib['id'] for x in items),
            'folders': [{'path': x['path'], 'mapped': mapped(x['path'], mounts) is not None,
                         'accessible': bool(mapped(x['path'], mounts) and mapped(x['path'], mounts).is_dir())}
                        for x in folders if x['libraryId'] == lib['id']]})
    files = {'tracked': 0, 'accessible': 0, 'unmapped': 0, 'missing': 0, 'bytes': 0,
             'items_marked_missing': sum(bool(x['isMissing']) for x in items),
             'items_marked_invalid': sum(bool(x['isInvalid']) for x in items), 'extensions': {}}
    for item in items:
        for f in decoded(item['libraryFiles'], []):
            meta = f.get('metadata', {})
            path = meta.get('path') or f.get('path')
            files['tracked'] += 1
            if not path:
                files['unmapped'] += 1
                continue
            ext = Path(path).suffix.lower()
            files['extensions'][ext] = files['extensions'].get(ext, 0) + 1
            target = mapped(path, mounts)
            if target is None:
                files['unmapped'] += 1
            elif target.is_file():
                files['accessible'] += 1
                files['bytes'] += target.stat().st_size
            else:
                files['missing'] += 1
    report['files'] = files
    users = rows(db, 'users')
    progress = rows(db, 'mediaProgresses')
    report['user_state'] = {'accounts': len(users), 'active_accounts': sum(bool(x['isActive']) for x in users),
        'bookmarks': sum(len(decoded(x['bookmarks'], [])) for x in users),
        'progress': len(progress), 'finished': sum(bool(x['isFinished']) for x in progress),
        'ebook_progress': sum(bool(x['ebookLocation'] or x['ebookProgress']) for x in progress),
        'listening_sessions': counts['playbackSessions']}
    report['relations'] = {'orphan_progress_users': db.execute('SELECT count(*) FROM mediaProgresses p LEFT JOIN users u ON u.id=p.userId WHERE u.id IS NULL').fetchone()[0],
        'orphan_library_items': db.execute('SELECT count(*) FROM libraryItems i LEFT JOIN libraries l ON l.id=i.libraryId WHERE l.id IS NULL').fetchone()[0]}
    return report


def snapshot(source, target):
    fd = os.open(target, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    os.close(fd)
    try:
        with connect(source) as src, sqlite3.connect(target) as dst:
            src.backup(dst)
            result = dst.execute('PRAGMA integrity_check').fetchall()
            if result != [('ok',)]:
                raise ValueError('snapshot failed integrity check')
    except BaseException:
        Path(target).unlink(missing_ok=True)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--database', required=True)
    parser.add_argument('--mounts', help='JSON object of container directory to host directory')
    parser.add_argument('--snapshot-dir', help='Create a NEW private snapshot directory; never overwrites')
    parser.add_argument('--pocket-database', help='Also snapshot Pocket Audiobooks, when --snapshot-dir is set')
    args = parser.parse_args()
    mounts = json.loads(Path(args.mounts).read_text()) if args.mounts else {}
    source = args.database
    if args.snapshot_dir:
        folder = Path(args.snapshot_dir)
        folder.mkdir(mode=0o700, parents=False, exist_ok=False)
        snapshot(source, folder / 'audiobookshelf.sqlite')
        source = folder / 'audiobookshelf.sqlite'
        if args.pocket_database:
            snapshot(args.pocket_database, folder / 'pocket-audiobooks.sqlite')
    with connect(source) as db:
        report = audit(db, mounts)
    body = json.dumps(report, indent=2) + '\n'
    if args.snapshot_dir:
        path = Path(args.snapshot_dir) / 'inventory.json'
        fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(fd, 'w') as out:
            out.write(body)
    print(body, end='')
    return 1 if report['integrity'] != ['ok'] or report['missing_required_tables'] else 0


if __name__ == '__main__':
    sys.exit(main())
