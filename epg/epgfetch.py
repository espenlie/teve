#!/usr/bin/env python
from lxml import objectify
import json
import urllib2
import time
from datetime import datetime, timedelta
import gzip
import StringIO
from dateutil import parser
import psycopg2
import sys

def main():
    # Parse the config file.
    if len(sys.argv) < 2:
      print "No config file specified. Exiting."
      sys.exit(1)
    config = json.load(open(sys.argv[1]))

    # Get the number of days to fetch EPG-data for in argv 2.
    days = 1
    if len(sys.argv) == 3:
      days = int(sys.argv[2])

    channels = [
            { 'epg': 'aljazeera.net', 'ui': 'Al Jazeera Intl'},
            { 'epg': 'nrk1.nrk.no',   'ui': 'NRK1 HD'},
            { 'epg': 'nrk1.nrk.no',   'ui': 'NRK1 Midtnytt'},
            { 'epg': 'nrk2.nrk.no',   'ui': 'NRK2'},
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

    for date in dates:
        for channel in channels:
            resp = urllib2.urlopen("%s/%s_%s.xml.gz" % (base, channel["epg"], date.strftime("%Y-%m-%d")))
            compr = StringIO.StringIO()
            compr.write(resp.read())
            compr.seek(0)

            f = gzip.GzipFile(fileobj=compr, mode='rb')
            root = objectify.fromstring(f.read())
            # print unicode(root["programme"]["title"])
            if not 'programme' in [el.tag for el in root.iterchildren()]:
                continue
            for programme in root["programme"]:
                d = {}
                start = parser.parse(programme.attrib["start"]).isoformat()
                stop = parser.parse(programme.attrib["stop"]).isoformat()
                title = unicode(programme["title"])
                ch = channel["ui"]
                stmt = "INSERT INTO %s(start,stop,title,channel) " % (config["DBName"])
                cur.execute(stmt + "VALUES(%s,%s,%s,%s)",(start,stop,title,ch))
    conn.commit()
    cur.close()
    conn.close()

if __name__ == "__main__":
    main()
