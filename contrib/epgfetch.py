#!/usr/bin/env python
from lxml import objectify
from datetime import datetime, timedelta
from dateutil import parser
import json, urllib2, time, gzip, StringIO, psycopg2, sys

def main():
    # Parse the config file.
    if len(sys.argv) < 2:
      print "No config file specified. Exiting."
      sys.exit(1)
    config = json.load(open(sys.argv[1]))

    # Get the number of days to fetch EPG-data for in config.
    days = config.get("EpgFetchDays", 4)

    modus = config.get("EPGmode", "js.gz") # or 'xml.gz'

    # Cache or something for each channel, so we dont do dupliacte requests.
    channel_cache = {}

    channels = [
            { 'epg': 'aljazeera.net', 'ui': 'Al Jazeera Intl'},
            { 'epg': 'nrk1.nrk.no',   'ui': 'NRK1 HD'},
            { 'epg': 'nrk1.nrk.no',   'ui': 'NRK1 Midtnytt'},
            { 'epg': 'nrk2.nrk.no',   'ui': 'NRK2'},
            { 'epg': 'nrk2.nrk.no',   'ui': 'NRK2 HD'},
            { 'epg': 'nrk3.nrk.no',   'ui': 'NRK3 HD'},
            { 'epg': 'no.bbchd.no',   'ui': 'BBC World News'},
            { 'epg': 'film.tv2.no',   'ui': 'TV2 Film'},
            { 'epg': 'bliss.tv2.no',  'ui': 'TV2 Bliss'},
            { 'epg': 'tv2.no',        'ui': 'TV2'},
            { 'epg': 'tv2.no',        'ui': 'TV2 HD'},
            { 'epg': 'news.tv2.no',   'ui': 'TV2 Nyheter'},
            { 'epg': 'sport.tv2.no',  'ui': 'TV2 Sport'},
            { 'epg': 'pl1.tv2.no',    'ui': 'TV2 Premium'},
            { 'epg': 'pl2.tv2.no',    'ui': 'TV2 Premium2'},
            { 'epg': 'pl3.tv2.no',    'ui': 'TV2 Premium3'},
            { 'epg': 'pl1.tv2.no',    'ui': 'TV2 Premium HD'},
            { 'epg': 'pl2.tv2.no',    'ui': 'TV2 Premium2 HD'},
            { 'epg': 'pl3.tv2.no',    'ui': 'TV2 Premium3 HD'},
            { 'epg': 'zebra.tv2.no',  'ui': 'TV2 Zebra'},
            { 'epg': 'supertv.nrk.no', 'ui': 'NRK Super'},
            { 'epg': 'cnn.com',       'ui': 'CNN International'},
    ]

    base = "http://xmltv.xmltv.se" if modus == "xml.gz" else "http://json.xmltv.se"
    dates = [datetime.today() + timedelta(days=i) for i in range(0,days)]
    conn = psycopg2.connect("host=%s dbname=%s user=%s password=%s" % (config["DBHost"], config["DBName"], config["DBUser"], config["DBPass"]))
    cur = conn.cursor()

    for channel in channels:
        # Delete all existing data for this channel in epg-db.
        cur.execute("DELETE FROM epg WHERE channel=%s", (channel["ui"],))
        conn.commit()

        progs = []
        for date in dates:
          # Get from cache, or add it to cache if not found.
          channel_key = channel["epg"] + "_" + date.strftime("%Y-%m-%d")
          if channel_key not in channel_cache:
            try:
              resp = urllib2.urlopen("%s/%s.%s" % (base, channel_key, modus))
            except Exception as exp:
              print "Got exception fetching %s" % channel_key
              raise(exp)
            compr = StringIO.StringIO()
            compr.write(resp.read())
            compr.seek(0)
            f = gzip.GzipFile(fileobj=compr, mode='rb')
            channel_cache[channel_key] = f.read()

          progs.extend(parse_channel(channel_cache[channel_key], channel, mode=modus))
          time.sleep(0.05)

        # Insert all found programmes, with a multi-insert.
        insert_vals = []
        for p in progs:
          insert = (p["start"],p["stop"],p["title"].strip(),channel["ui"],p["description"].strip())
          insert_vals.append(insert)
        args_str = ",".join(cur.mogrify("(%s,%s,%s,%s,%s)", x) for x in insert_vals)
        cur.execute("INSERT INTO epg(start,stop,title,channel,description) VALUES" + args_str)
    conn.commit()
    cur.close()
    conn.close()

def parse_channel(inp, channel, mode="xml.gz"):
  programmes = []
  if mode == "xml.gz":
    root = objectify.fromstring(inp)
    if not hasattr(root, 'programme'):
        return
    for programme in root["programme"]:
        d = {}
        d["start"] = parser.parse(programme.attrib["start"]).isoformat()
        d["stop"] = parser.parse(programme.attrib["stop"]).isoformat()
        d["title"] = unicode(programme["title"])
        d["description"] = unicode(programme["desc"]) if hasattr(programme, "desc") else ""
        programmes.append(d)
  elif mode == "js.gz":
    root = json.loads(inp)["jsontv"]
    if not "programme" in root:
      return
    for programme in root["programme"]:
      d = {}
      # Convert times from epoch.
      d["start"] = time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(int(programme["start"])))
      d["stop"] = time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(int(programme["stop"])))

      # They differientiate betwen english and norwegian titles. We preffer norwegian.
      titles = programme.get("title", {})
      d["title"] = unicode(titles.get("no")) if titles.get("no") else unicode(titles.get("en", ""))

      # Same with descriptions.
      descriptions = programme.get("desc", {})
      d["description"] = unicode(descriptions.get("no")) if descriptions.get("no") else unicode(descriptions.get("en", ""))
      programmes.append(d)
  return programmes

if __name__ == "__main__":
    main()
