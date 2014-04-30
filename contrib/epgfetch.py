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
    days = config["EpgFetchDays"] if "EpgFetchDays" in config else 4

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
    base = "http://xmltv.xmltv.se"
    dates = [datetime.today() + timedelta(days=i) for i in range(0,days)]
    conn = psycopg2.connect("host=%s dbname=%s user=%s password=%s" % (config["DBHost"], config["DBName"], config["DBUser"], config["DBPass"]))
    cur = conn.cursor()

    # Delete all existing data in epg-db.
    cur.execute("DELETE FROM epg")

    for date in dates:
        for channel in channels:
            # Get from cache, or add it to cache if not found.
            channel_key = channel["epg"] + "_" + date.strftime("%Y-%m-%d")
            if channel_key not in channel_cache:
              resp = urllib2.urlopen("%s/%s.xml.gz" % (base, channel_key))
              compr = StringIO.StringIO()
              compr.write(resp.read())
              compr.seek(0)
              f = gzip.GzipFile(fileobj=compr, mode='rb')
              channel_cache[channel_key] = objectify.fromstring(f.read())
            root = channel_cache[channel_key]

            if not hasattr(root, 'programme'):
                continue
            for programme in root["programme"]:
                d = {}
                start = parser.parse(programme.attrib["start"]).isoformat()
                stop = parser.parse(programme.attrib["stop"]).isoformat()
                title = unicode(programme["title"])
                description = unicode(programme["desc"]) if hasattr(programme, "desc") else ""
                ch = channel["ui"]
                cur.execute("INSERT INTO epg(start,stop,title,channel,description) VALUES(%s,%s,%s,%s,%s)",(start,stop,title.strip(),ch,description))
    conn.commit()
    cur.close()
    conn.close()

if __name__ == "__main__":
    main()
