"""Ordered, checksum-checked SQLite migrations under the process write lock."""
import hashlib
import re
import sqlite3
from pathlib import Path

MIGRATIONS_DIR = Path(__file__).parent / 'sqlite_migrations'
_FILENAME = re.compile(r'^(?P<version>\d{4})_(?P<name>[a-z0-9_]+)\.sql$')


def _checksum(text):
    return hashlib.sha256(text.encode()).hexdigest()


def discover():
    found = {}
    for path in sorted(MIGRATIONS_DIR.glob('*.sql')):
        match = _FILENAME.match(path.name)
        if not match or match['version'] in found:
            raise ValueError(f'Invalid or duplicate migration: {path.name}')
        found[match['version']] = (match['name'], path)
    return [(version, name, path) for version, (name, path) in sorted(found.items())]


def applied(conn):
    conn.execute('''CREATE TABLE IF NOT EXISTS schema_migrations (
        version TEXT PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL,
        applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)''')
    return dict(conn.execute('SELECT version,checksum FROM schema_migrations').fetchall())


def statements(sql):
    pending = ''
    for line in sql.splitlines(keepends=True):
        pending += line
        if sqlite3.complete_statement(pending):
            yield pending
            pending = ''
    if pending.strip():
        raise ValueError('Incomplete migration statement')


def ensure_current(conn):
    with conn.transaction():
        already = applied(conn)
        ran = 0
        for version, name, path in discover():
            sql = path.read_text()
            digest = _checksum(sql)
            if version in already:
                if already[version] != digest:
                    raise RuntimeError(f'Applied migration {path.name} has changed')
                continue
            for statement in statements(sql):
                conn.execute(statement)
            conn.execute('INSERT INTO schema_migrations(version,name,checksum) VALUES(?,?,?)',
                         (version, name, digest))
            ran += 1
        return ran
