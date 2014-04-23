#!/bin/bash
BASEDIR=$(dirname $0)
psql -f $BASEDIR/db.sql -d epg
python $BASEDIR/epgfetch.py $BASEDIR/../config.json 1
