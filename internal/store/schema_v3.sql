-- Messages go to the file only for kept chats; see the message storage specification.

CREATE TABLE kept (
  chat TEXT PRIMARY KEY
);

-- The chats that have a route stay kept when an installation upgrades.
INSERT INTO kept (chat) SELECT chat FROM routes WHERE chat <> '*';

-- WhatsAgent no longer stores the history that WhatsApp sends at linking, so every event is live.
DELETE FROM events WHERE live = 0;

ALTER TABLE events DROP COLUMN live;
