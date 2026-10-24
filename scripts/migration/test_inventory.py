import json
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest

from inventory import audit, connect, mapped, snapshot


class InventoryTests(unittest.TestCase):
    def test_snapshot_preserves_source_and_is_private(self):
        with tempfile.TemporaryDirectory() as tmp:
            source, dest = Path(tmp)/'source.sqlite', Path(tmp)/'copy.sqlite'
            with sqlite3.connect(source) as db:
                db.execute('CREATE TABLE users (secret TEXT)')
                db.execute("INSERT INTO users VALUES ('private credential')")
            before = source.read_bytes()
            snapshot(source, dest)
            self.assertEqual(source.read_bytes(), before)
            self.assertEqual(dest.stat().st_mode & 0o777, 0o600)
            with connect(dest) as db:
                self.assertEqual(db.execute('SELECT secret FROM users').fetchone()[0], 'private credential')
                with self.assertRaises(sqlite3.OperationalError):
                    db.execute('DELETE FROM users')
            with self.assertRaises(FileExistsError):
                snapshot(source, dest)

    def test_paths_are_bound_to_the_correct_mount(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)/'media'
            root.mkdir()
            mounts={'/books':str(root), '/books/archive':str(root/'archive')}
            self.assertEqual(mapped('/books/title/book.m4b', mounts), root/'title/book.m4b')
            self.assertEqual(mapped('/books/archive/title.mp3', mounts), root/'archive/title.mp3')
            self.assertIsNone(mapped('/books-other/book.m4b', mounts))
            self.assertIsNone(mapped('/books/../secret', mounts))
            (root/'escape').symlink_to(Path(tmp))
            self.assertIsNone(mapped('/books/escape/secret', mounts))

    def test_report_omits_credentials_and_personal_account_details(self):
        with sqlite3.connect(':memory:') as db:
            db.executescript('''
                CREATE TABLE libraries(id TEXT, name TEXT, mediaType TEXT);
                CREATE TABLE libraryFolders(id TEXT, libraryId TEXT, path TEXT);
                CREATE TABLE libraryItems(id TEXT, libraryId TEXT, libraryFiles TEXT, isMissing INT, isInvalid INT);
                CREATE TABLE books(id TEXT);
                CREATE TABLE users(id TEXT, username TEXT, email TEXT, token TEXT, pash TEXT, bookmarks TEXT, isActive INT);
                CREATE TABLE mediaProgresses(id TEXT, userId TEXT, isFinished INT, ebookLocation TEXT, ebookProgress REAL);
                CREATE TABLE playbackSessions(id TEXT);
                INSERT INTO libraries VALUES ('lib', 'Books', 'book');
                INSERT INTO users VALUES ('u', 'private username', 'private@example.com', 'private token', 'private hash', '[{"title":"private bookmark"}]', 1);
                INSERT INTO mediaProgresses VALUES ('p', 'u', 0, '', 0);
            ''')
            report = audit(db, {})
            text = json.dumps(report)
            self.assertNotIn('private', text)
            self.assertEqual(report['user_state']['bookmarks'], 1)
            self.assertEqual(report['user_state']['progress'], 1)
            self.assertEqual(report['relations']['orphan_progress_users'], 0)

    def test_unknown_schema_is_reported_not_silently_accepted(self):
        with sqlite3.connect(':memory:') as db:
            report = audit(db, {})
            self.assertIn('libraryItems', report['missing_required_tables'])


if __name__ == '__main__':
    unittest.main()
