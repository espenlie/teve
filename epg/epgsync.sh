#!/bin/bash
BASEDIR=$(dirname $0)
psql -f $BASEDIR/db.sql -d epg
python $BASEDIR/epgfetch.py $BASEDIR/../config.json 4
python $BASEDIR/check_subscriptions.py $BASEDIR/../config.json
python $BASEDIR/get_nrk.py $BASEDIR/../config.json
