CREATE TABLE chats (
  jid TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  last_time INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE messages (
  chat TEXT NOT NULL,
  id TEXT NOT NULL,
  sender TEXT NOT NULL DEFAULT '',
  raw_sender TEXT NOT NULL DEFAULT '',
  sender_name TEXT NOT NULL DEFAULT '',
  time INTEGER NOT NULL,
  from_me INTEGER NOT NULL DEFAULT 0,
  role TEXT NOT NULL DEFAULT 'other',
  text TEXT NOT NULL DEFAULT '',
  reply_to TEXT NOT NULL DEFAULT '',
  quote TEXT NOT NULL DEFAULT '',
  mentions TEXT NOT NULL DEFAULT '',
  media_type TEXT NOT NULL DEFAULT '',
  mime_type TEXT NOT NULL DEFAULT '',
  filename TEXT NOT NULL DEFAULT '',
  reaction_to TEXT NOT NULL DEFAULT '',
  reaction TEXT NOT NULL DEFAULT '',
  forwarded INTEGER NOT NULL DEFAULT 0,
  edited INTEGER NOT NULL DEFAULT 0,
  deleted INTEGER NOT NULL DEFAULT 0,
  raw BLOB,
  PRIMARY KEY (chat, id)
);

CREATE INDEX messages_by_time ON messages (chat, time);

CREATE TABLE events (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  chat TEXT NOT NULL,
  message_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  live INTEGER NOT NULL
);

CREATE INDEX events_by_chat ON events (chat, seq);

CREATE TABLE routes (
  chat TEXT PRIMARY KEY,
  session TEXT NOT NULL,
  prefix TEXT NOT NULL DEFAULT '',
  addressed INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE send_rules (
  session TEXT NOT NULL,
  chat TEXT NOT NULL,
  mode TEXT NOT NULL,
  PRIMARY KEY (session, chat)
);

CREATE TABLE sessions (
  name TEXT PRIMARY KEY,
  harness TEXT NOT NULL DEFAULT '',
  folder TEXT NOT NULL DEFAULT '',
  last_seen INTEGER NOT NULL DEFAULT 0,
  cursor INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE sent (
  id TEXT PRIMARY KEY,
  chat TEXT NOT NULL,
  time INTEGER NOT NULL
);

CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
