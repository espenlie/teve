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

Finally, you need users. They need to match the users defined in the
`config.json` file and the first user is created by:

    $ htpasswd -c .htpasswd username

Next users are created by:

    $ htpasswd .htpasswd username

Finally, the `recordings/` folder need to be created:

    $ mkdir recordings

### Update EPG-data

Run this command once a day:

`$ ./epg/epgsync`

To get started, you may find it simplest to just start a virtualenv:

    $ virtualenv --system-site-packages epg/venv
    $ source epg/venv/bin/activate
    $ pip install -r epg/requirements.txt

### Sample Nginx-setup

    server {
        listen 80;
        server_name fqdn;

        access_log /var/log/nginx/fgdn.access.log;
        error_log /var/log/nginx/fqdn.error.log;
        location /tv/ {
            proxy_connect_timeout 5;
            proxy_pass http://localhost:12000/;
        } 
        location /tv/static {
            alias /srv/teve/static;
            expires 14d;
        }
    }

### Dependencies

Python:

- lxml
- python-dateutil
- psycopg2

Golang:

- [pq](http://godoc.org/github.com/lib/pq)
- [go-http-auth](https://github.com/abbot/go-http-auth/)

Unix:

- PostgreSQL (`apt-get install postgresql`)
