-- Delete all old EPG-data and just start anew.
DROP TABLE IF EXISTS epg;
CREATE TABLE epg (
  title text,
  start timestamp,
  stop timestamp,
  channel varchar(30),
  description text
);

CREATE TABLE IF NOT EXISTS recordings (
  id serial primary key,
  start timestamp,
  stop timestamp,
  username varchar(20),
  title varchar(256),
  channel varchar(30),
  transcode varchar(4)
);

CREATE TABLE IF NOT EXISTS subscriptions (
  id serial primary key,
  title text,
  interval_start smallint,
  interval_stop smallint,
  weekday smallint,
  channel varchar(30),
  username varchar(20),
  unique(interval_start, interval_stop)
);

-- Ensure that two users dont subscribe to the same program. Unecessary, as
-- both users access the same archive.
CREATE UNIQUE INDEX unique_subscription ON subscriptions(title, weekday, channel);
