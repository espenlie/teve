#!/bin/bash
BASEDIR=$(pwd -P)
CONFIG=$BASEDIR/../config.json

# Run various scripts, fetching EPG and so forth.
python $BASEDIR/epgfetch.py $CONFIG
python $BASEDIR/check_subscriptions.py $CONFIG
python $BASEDIR/get_nrk.py $CONFIG
