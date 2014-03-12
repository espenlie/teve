#!/bin/bash
psql -f db.sql -d epg
./epgfetch.py
