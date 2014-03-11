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

### Update EPG-data

Run this command once a day:

`$ ./epgsync epg.db`

### Dependencies

Python:

- lxml
- python-dateutil
- sqlite3

Golang:

- (sqlite3)[github.com/mattn/go-sqlite3]
