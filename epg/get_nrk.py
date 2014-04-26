#!/usr/bin/env python
import urllib, json, sys

channels = {
    "NRK2 HD": ["https://nrk2us-f.akamaihd.net/i/nrk2us_0@107231/master.m3u8?dw=31", "#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=3528000,RESOLUTION=1280x720"],
    "NRK3 HD": ["https://nrk3us-f.akamaihd.net/i/nrk3us_0@107233/master.m3u8?dw=31", "#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=3528000,RESOLUTION=1280x720"]
}
stream_urls = {}

# Parse the config file.
if len(sys.argv) < 2:
  print "No config file specified. Exiting."
  sys.exit(1)
config = json.load(open(sys.argv[1]))

for cname, params in channels.iteritems():
    m3u_file = params[0]
    quality = params[1]

    resp = urllib.urlopen(m3u_file)
    urls = resp.readlines()
    for i, line in enumerate(urls):
        if line.strip() == quality:
            stream_urls[cname] = urls[i+1].strip()

port = ":%s" % config["WebPort"] if config["Debug"] else ""
base_url = "http://%s%s%s" % (config["Hostname"], port, config["BaseUrl"])
api_endpoint = "addChannel"

for cname, url in stream_urls.iteritems():
    if cname in [c["Name"] for c in config["Channels"]]:
        params = { 'cname': cname, 'url': url }
        endpoint = base_url + api_endpoint + "?" + urllib.urlencode(params)
        try:
            urllib.urlopen(endpoint)
        except:
            pass
