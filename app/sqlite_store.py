"""SQLite connections: explicit transactions, typed dates and one process write lock."""
import datetime as dt
import json
import math
import sqlite3
from contextlib import contextmanager
from pathlib import Path

import numpy as np
import pandas as pd

from app.resources import shared_write_lock as get_write_lock


def timestamp(value):
    stamp = pd.Timestamp(value)
    if stamp.tzinfo is not None:
        stamp = stamp.tz_convert('UTC').tz_localize(None)
    return stamp.strftime('%Y-%m-%d %H:%M:%S.%f')


def adapt(value):
    if value is None or value is pd.NaT:
        return None
    if isinstance(value, (pd.Timestamp, dt.datetime)):
        return timestamp(value)
    if isinstance(value, dt.date):
        return value.isoformat()
    if isinstance(value, (list, tuple)):
        return json.dumps([adapt(v) for v in value])
    if isinstance(value, np.generic):
        value = value.item()
    if isinstance(value, float) and not math.isfinite(value):
        return None
    return value


def parameters(values):
    if isinstance(values, dict):
        return {key: adapt(value) for key, value in values.items()}
    return tuple(adapt(value) for value in values)


sqlite3.register_converter('TIMESTAMP', lambda b: dt.datetime.fromisoformat(b.decode()))
sqlite3.register_converter('DATE', lambda b: dt.date.fromisoformat(b.decode()))
sqlite3.register_converter('JSON', lambda b: json.loads(b.decode()))


class Stddev:
    def __init__(self):
        self.n = 0
        self.mean = self.m2 = 0.0

    def step(self, value):
        if value is not None:
            self.n += 1
            delta = value - self.mean
            self.mean += delta / self.n
            self.m2 += delta * (value - self.mean)

    def finalize(self):
        return math.sqrt(max(0, self.m2) / (self.n - 1)) if self.n > 1 else None


class Cursor(sqlite3.Cursor):
    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()

    def execute(self, sql, params=()):
        with get_write_lock():
            return super().execute(sql, parameters(params))

    def executemany(self, sql, rows):
        with get_write_lock():
            return super().executemany(sql, (parameters(row) for row in rows))


class Connection(sqlite3.Connection):
    def cursor(self, factory=Cursor):
        return super().cursor(factory)

    def execute(self, sql, params=()):
        return self.cursor().execute(sql, params)

    def executemany(self, sql, rows):
        return self.cursor().executemany(sql, rows)

    def commit(self):
        with get_write_lock():
            super().commit()

    def rollback(self):
        with get_write_lock():
            super().rollback()

    @contextmanager
    def transaction(self):
        with get_write_lock():
            nested = self.in_transaction
            savepoint = 'nested_desktop'
            self.execute(f'SAVEPOINT {savepoint}' if nested else 'BEGIN IMMEDIATE')
            try:
                yield self
                self.execute(f'RELEASE {savepoint}' if nested else 'COMMIT')
            except BaseException:
                if nested:
                    self.execute(f'ROLLBACK TO {savepoint}')
                    self.execute(f'RELEASE {savepoint}')
                else:
                    self.rollback()
                raise


def connect(path, *, readonly=False):
    if not readonly and str(path) != ':memory:':
        Path(path).parent.mkdir(parents=True, exist_ok=True)
    filename = Path(path).resolve().as_uri() + '?mode=ro' if readonly else str(path)
    conn = sqlite3.connect(filename, uri=readonly, factory=Connection, isolation_level=None,
                           check_same_thread=False, timeout=10,
                           detect_types=sqlite3.PARSE_DECLTYPES | sqlite3.PARSE_COLNAMES)
    if readonly:
        conn.execute('PRAGMA query_only=ON')
    else:
        conn.execute('PRAGMA journal_mode=WAL')
        conn.execute('PRAGMA synchronous=FULL')
    conn.execute('PRAGMA foreign_keys=ON')
    conn.execute('PRAGMA busy_timeout=10000')
    conn.create_aggregate('stddev', 1, Stddev)
    return conn
