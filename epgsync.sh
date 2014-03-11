#!/bin/bash
if [ $# -ne 1 ]; then
  echo "You need to specify a database to update. ./epgsync dbname";
  exit 2;
fi

sqlite3 tmp.db < db.sql
./epgfetch.py tmp.db
mv tmp.db $1
