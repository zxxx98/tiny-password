CREATE TABLE parents (
    id INTEGER PRIMARY KEY
);

CREATE TABLE things (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    parent_id INTEGER NOT NULL REFERENCES parents (id),
    name      TEXT NOT NULL
);

CREATE TABLE counters (
    id INTEGER PRIMARY KEY,
    n  INTEGER NOT NULL DEFAULT 0
);

INSERT INTO counters (id, n) VALUES (1, 0);
