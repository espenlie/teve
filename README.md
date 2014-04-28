# teve

A HTTP streaming frontend for VLC, with features such as record/archive and
subscriptions - making it a complete DVR-solution for HTTP/Multicast streams.

The frontend language in Norwegian, but for some reason the README is in
English.

A working demo is available at: [http://demo:password@tits.abakus.no/tv](http://demo:password@tits.abakus.no/tv)

## Building and configuration

First, make sure all dependencies are met:

    $ aptitude install postgresql postgresql-server-dev-all python-dev apache-utils libxslt-devel

Then create the postgres-user and give the correct permissions:

    $ sudo su postgres -s /bin/bash
    $ createuser tvadmin -P -D -R -S
    $ createdb teve -O tvadmin

Edit the configuration file with your preferred contents and more importantly
the DB password/username from previous step and the hostname:

    $ cp config.json.example config.json

Run the various scripts in order to fetch EPG-data, set correct URLs and so
forth. For this you may want to create a virtual environment, if not you can
skip the two first steps:

    $ virtualenv contrib/venv
    $ source contrib/venv/bin/activate
    $ pip install -r contrib/requirements.txt
    $ ./contrib/sync.sh [ tvadmin ]

Create your first user for the systems basic auth by creating a .htpasswd file:

    $ htpasswd -c .htpasswd username

Fetch Go dependencies and build the binary. Make sure you have set the GOPATH
correctly, here we assume the source code for `teve` is in `$HOME/go/src/teve`:

    $ export GOPATH=$HOME/go
    $ go get
    $ go build
    $ ./teve

## Ensuring updated data

Add the `contrib/sync.sh` to your crontab, by adding a symlink to it in your
`/etc/cron.daily`:

    ln -s contrib/sync.sh /etc/cron.daily/teve

## Using cubemap

Cubemap is a high-performance, high-availability video reflector for VLC, which
allows you to keep a connection alive with clients as you change the channel or
transcoding. Further it paces TCP-packets in a fashion more useful for
video-streaming, instead of sending packets in bursts. Read more in the [README
for the project](http://git.sesse.net/?p=cubemap;a=blob_plain;f=README;hb=HEAD)

Installing cubmap is pretty straight forward. You want VLC 2.2.0 or newer and a
pretty modern kernel version. If you have an older VLC you might want to build
it from source:

    $ git clone git://git.videolan.org/vlc.git
    $ cd vlc/
    $ ./bootstrap
    $ ./configure --disable-dbus --disable-mad --disable-postproc --disable-a52 --disable-glx --disable-fribidi --disable-qt --disable-skins2 --enable-dvbpsi --enable-faad --disable-nls --disable-xcb --disable-sdl --disable-libgcrypt --disable-lua --disable-alsa --disable-v4l2 --enable-libgcrypt
    $ make -j4
    $ sudo make install
    $ sudo ldconfig

Then you want to install cubemap:

    $ sudo aptitude install libprotobuf-dev protobuf-compiler
    $ git clone http://git.sesse.net/cubemap
    $ cd cubemap
    $ make -j4
    $ make install

Then create a working-space for cubemap in the project-folder:

    $ mkdir cubemap

Start *teve* with the `--cubemap` flag:

    $ ./teve --cubemap cubemap/cubemap.config

This will fail the first time, but it will create a config-file. Then you can
start the cubemap service and the teve-service, this time working:

    $ cubemap cubemap/cubemap.config
    $ ./teve --cubemap cubemap/cubemap.config

### Sample Nginx-setup

We here assume that the base-path is set to `/tv/`, that the service is running
at port 12000 and that the source code is found at `/srv/teve`. `fqn` means
*fully qualified domain name* and should be the domain + subdomain that you are
running the service on.

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
        location /tv/favicon.ico {
            alias /srv/teve/static/favicon.ico;
            expires 30d;
        }
    }

### Go dependencies

These are downloaded and built with the `go get` command.

- [pq](http://godoc.org/github.com/lib/pq)
- [go-http-auth](https://github.com/abbot/go-http-auth/)
