DROP TABLE IF EXISTS epg;
CREATE TABLE epg (
  title text,
  start timestamp,
  stop timestamp,
  channel varchar(30)
);
CREATE TABLE IF NOT EXISTS recordings (
  id serial primary key,
  start timestamp,
  stop timestamp,
  username varchar(20),
  title varchar(256),
  channel varchar(30),
  transcode varchar(4),
  unique(start,title,channel)
);
CREATE TABLE IF NOT EXISTS subscriptions (
  title text,
  interval_start smallint,
  interval_stop smallint,
  weekday varchar(8),
  username varchar(20),
  unique(title,interval_start,interval_stop,weekday,username)
);
GRANT ALL ON epg TO epguser;
GRANT ALL ON recordings TO epguser;
GRANT ALL ON recordings_id_seq TO epguser;
GRANT ALL ON subscriptions TO epguser;
