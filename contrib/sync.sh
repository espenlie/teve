#!/bin/bash

# Check if we pass a user that owns the teve tables.
USER=tvadmin
if [ "$#" -eq 1 ]; then
  USER=$1
fi

BASEDIR=$(dirname $0)
CONFIG=$BASEDIR/../config.json

# Update the DB
psql -U $USER -f $BASEDIR/db.sql -d teve

# Run various scripts, fetching EPG and so forth.
python $BASEDIR/epgfetch.py $CONFIG
python $BASEDIR/check_subscriptions.py $CONFIG
python $BASEDIR/get_nrk.py $CONFIG
