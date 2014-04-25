#!/usr/bin/env python
import urllib2, json, sys

config = json.load(open(sys.argv[1]))
hname = config["Hostname"]
port = config["WebPort"]
url = config["BaseUrl"]
try:
  urllib2.urlopen("http://%s:%s%s" % (hname, port, url) + "checkSubscriptions")
except:
  # Ignore all errors
  pass
