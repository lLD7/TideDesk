-- Durable slot claims prevent duplicate automatic collections after restart.
CREATE TABLE IF NOT EXISTS desktop_schedule_runs (
    slot_key TEXT PRIMARY KEY,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    status TEXT NOT NULL
);
