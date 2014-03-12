DROP TABLE IF EXISTS epg;
CREATE TABLE epg (
  title text,
  start timestamp,
  stop timestamp,
  channel varchar(30)
);
GRANT ALL ON epg TO epguser;
