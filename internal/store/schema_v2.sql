-- Chat modes replace the addressed flag of routes, and send rules name their outcome a permission.

CREATE TABLE chat_modes (
  chat TEXT PRIMARY KEY,
  mode TEXT NOT NULL,
  length INTEGER NOT NULL DEFAULT 0,
  lookback INTEGER NOT NULL DEFAULT 0
);

-- Routes delivered every message unless they were addressed, which is the keyword mode now.
INSERT INTO chat_modes (chat, mode)
  SELECT chat, CASE addressed WHEN 1 THEN 'keyword' ELSE 'all' END FROM routes;

ALTER TABLE routes DROP COLUMN addressed;

ALTER TABLE send_rules RENAME COLUMN mode TO permission;

CREATE TABLE windows (
  chat TEXT PRIMARY KEY,
  session TEXT NOT NULL,
  opened INTEGER NOT NULL,
  from_time INTEGER NOT NULL,
  until_time INTEGER NOT NULL
);

CREATE TABLE chat_agents (
  chat TEXT PRIMARY KEY,
  session TEXT NOT NULL UNIQUE
);
