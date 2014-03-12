# teve

### Start TV-service

If running for first time, then first install go packages:

`$ go get`

If rebuilding binary then build first by:

`$ go build`

Then

`$ ./teve`

### Configuration

You need add users and define your *hostname* in the `config.json` file.

You also need a Postgresql DB called `epg` and a postgresql user called
`epguser`.
These are simply created by:

    $ createdb epg
    $ createuser epguser

### Update EPG-data

Run this command once a day:

`$ ./epg/epgsync`

To get started, you may find it simplest to just start a virtualenv:

    $ virtualenv --system-site-packages epg/venv
    $ source epg/venv/bin/activate
    $ pip install -r epg/requirements.txt

### Dependencies

Python:

- lxml
- python-dateutil
- psycopg2

Golang:

- [pq](http://godoc.org/github.com/lib/pq)

Unix:

- PostgreSQL (`apt-get install postgresql`)
