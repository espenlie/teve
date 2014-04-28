package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	auth "github.com/abbot/go-http-auth"
	_ "github.com/lib/pq"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Channel struct {
	Name     string
	Address  string
	Running  bool
	Outgoing string
	Views    string
	EPGlist  []EPG
}

type File struct {
	Name string
	Size int64
	Url  string
	SUrl string
}

type User struct {
	Name string
	Id   string
}

type Config struct {
	Channels         *[]Channel
	Hostname         string
	HttpUser         string
	HttpPass         string
	BaseUrl          string
	StreamingPort    string
	SubIntervalSize  int
	WebPort          string
	RecordingsFolder string
	PasswordFile     string
	DBHost           string
	DBName           string
	DBUser           string
	DBPass           string
	Debug            bool
	CubemapConfig    string
	CubemapPort      int
}

type Command struct {
	Name      string
	Cmd       *exec.Cmd
	Transcode int
	Address   string
}

type EPG struct {
	Title       string
	Start       string
	Stop        string
	StartLong   string
	StopLong    string
	Description string
}

type Recording struct {
	Id          int64
	Channel     string
	Start       string
	Stop        string
	Title       string
	User        string
	Transcoding string
	Cmd         *exec.Cmd
}

type Subscription struct {
	Id        int64
	Title     string
	StartTime string
	Weekday   string
	Channel   string
}

var config Config

var streams = make(map[string]Command)
var recordings = make(map[int64]Recording)
var dbh *sql.DB

func ensureDbhConnection() {
	var err error
	if (dbh == nil) {
		// The DB has probably not been intitialized. Probably since we've just booted the application.
		dbh, err = getDatabaseHandler()
		if err != nil {
			logMessage("error", "Could not initialize DB-connection", err)
		}
	}
	for connerr := dbh.Ping(); connerr != nil; {
		// We can't seem to get a connection. Try to get it.
		dbh, err = getDatabaseHandler()
		if err != nil {
			// And we try again and again, until it responds.
			logMessage("warn", "Can't connect to the Postgresql DB, trying again in 2 seconds", err)
			time.Sleep(2 * time.Second)
		}
	}
}

func getDatabaseHandler() (*sql.DB, error) {
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		return nil, err
	}

	// Check that the DB is responding.
	err = dbh.Ping()
	if err != nil {
		return nil, errors.New(fmt.Sprintf("PostgreSQL not responding: %v", err))
	}

	return dbh, nil
}

func loadConfig(filename string) Config {
	file, err := ioutil.ReadFile(filename)
	if err != nil {
		logMessage("error", "Problemer med å lese config-fil", err)
	}
	err = json.Unmarshal(file, &config)
	if err != nil {
		logMessage("error", "Problemer med å pakke ut config", err)
	}
	return config
}

func logMessage(level, msg string, err error) {
	// Uniform level strings
	level = strings.ToUpper(level)

	// Get the error message
	e := ""
	if err != nil {
		e = ": " + err.Error()
	}

	// Quit the program if we get an error
	if level == "ERROR" {
		log.Fatalf("[%s] %s%s\n", level, msg, e)
	}

	// Else we just print the messgae to stdout.
	log.Printf("[%s] %s %s\n", level, msg, e)
}

func getTranscoding(trans string) int {
	// Returns 0 if we cant parse string, if not it returns parsed integer.
	transcoding := 0
	transcoding, _ = strconv.Atoi(trans)
	return transcoding
}

func getNorwegianWeekday(day int) string {
	dict := map[int]string{
		1: "mandag",
		2: "tirsdag",
		3: "onsdag",
		4: "torsdag",
		5: "fredag",
		6: "lørdag",
		0: "søndag",
	}

	if _, ok := dict[day]; !ok {
		return "???"
	}
	return dict[day]
}

func loadPlannedRecordings() error {
	ensureDbhConnection()

	long_form := "2006-01-02 15:04"

	// First delete those that have finished since last time.
	rows, err := dbh.Query("SELECT id FROM recordings WHERE stop < now()")
	if err != nil {
		return err
	}

	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		err := removeRecording(id)
		if err != nil {
			return err
		}
	}

	rows, err = dbh.Query("SELECT start,stop,username,title,channel,transcode FROM recordings")
	if err != nil { return err }

	cnt := 0
	for rows.Next() {
		var username, title, channel, transcode string
		var start, stop time.Time
		rows.Scan(&start, &stop, &username, &title, &channel, &transcode)
		go startRecording(start.Format(long_form), stop.Format(long_form), username, title, channel, transcode)
		cnt += 1
	}
	logMessage("info", fmt.Sprintf("Loaded %d recordings from DB", cnt), nil)
	return nil
}

func insertRecording(username, title, channel, transcode string, start, stop time.Time) (int64, error) {
	// We'll use the DB, so ensure it is up.
	ensureDbhConnection()

	tx, err := dbh.Begin()
	var id int64
	err = tx.QueryRow(`SELECT id FROM recordings
                     WHERE title = $1
                     AND channel = $2
                     AND start = $3
                     AND stop = $4`, title, channel, start, stop).Scan(&id)
	if err == sql.ErrNoRows {
		// Great the recording does not exist in the DB yet, lets insert it.
		err := dbh.QueryRow(`INSERT INTO recordings(
    start,stop,username,title,channel,transcode) VALUES
    ($1,$2,$3,$4,$5,$6) RETURNING id`,
			start, stop, username, title, channel, transcode).Scan(&id)
		if err != nil {
			return id, err
		}
	} else if err != nil {
		// There was an actual DB-error.
		return id, err
	}
	// We have either inserted the recording successfully, or the recording
	// already exists and we return the id.
	return id, nil
}

func removeRecording(id int64) error {
	// We'll use the DB, so ensure it is up.
	ensureDbhConnection()

	tx, _ := dbh.Begin()
	_, err := tx.Exec("DELETE FROM recordings WHERE id = $1", id)
	if err != nil { return err }
	_ = tx.Commit()

	// Delete from cmd and kill command.
	delete(recordings, id)
	return nil
}

func getVLCstr(transcoding int, address, dst, access string) string {
	transcoding_opts := fmt.Sprintf("#transcode{vcodec=mp2v,vb=%v,acodec=aac,ab=128,scale=0.7,threads=2}:", transcoding)
	output := fmt.Sprintf("std{access=%v,mux=ts,dst=%v}'", access, dst)
	command := fmt.Sprintf("cvlc '%v' --sout '", address)

	if transcoding != 0 {
		command += transcoding_opts
	} else {
		command += "#"
	}
	command += output
	return command
}

func startUniStream(channel Channel, user User, transcoding int, access string) (*exec.Cmd, error) {
	var cmd *exec.Cmd

	userSuffix := fmt.Sprintf(":%v%v/%v", config.StreamingPort, user.Id, user.Name)
	command := getVLCstr(transcoding, channel.Address, userSuffix, access)
	cmd = exec.Command("bash", "-c", command)
	err := cmd.Start()
	return cmd, err
}

func killUniStream(user User) error {
	var err error
	if _, ok := streams[user.Name]; ok {
		err = killStream(streams[user.Name].Cmd)
	}
	delete(streams, user.Name)
	return err
}

func killStream(cmd *exec.Cmd) error {
	if err := cmd.Process.Kill(); err != nil {
		return err
	}
	cmd.Process.Wait()
	return nil
}

func zeroPad(n string) string {
	// Makes the string '1' become '01'.
	if len(n) == 1 {
		return fmt.Sprintf("0%v", n)
	}
	return n
}

func getUserFromName(username string) (User, error) {
	// Creates a User-object and gives ID based on placement in PasswordFile.
	f, err := ioutil.ReadFile(config.PasswordFile)
	if err != nil { return User{}, err }

	lines := strings.Split(string(f), "\n")
	for id, line := range lines[0 : len(lines)-1] {
		s := strings.SplitN(line, ":", 2)
		strId := strconv.Itoa(id)
		if username == s[0] {
			strId = zeroPad(strId)
			return User{Name: username, Id: strId}, nil
		}
	}
	return User{}, errors.New("Did not find user '" + username + "' authenticated from Basic Auth.")
}

func getUserFromRequest(r *auth.AuthenticatedRequest) (User, error) {
	return getUserFromName(r.Username)
}

func getChannel(channel_name, username string) (*Channel, error) {
	// Check if the channel is defined in the config-file.
	arr := *(config.Channels)
	for i, _ := range arr {
		if arr[i].Name == channel_name {
			return &(arr[i]), nil
		}
	}

	// Check if the user is running a stream, that perhaps is not in the config file.
	if _, ok := streams[username]; ok {
		s := streams[username]
		return &(Channel{Name: s.Name, Address: s.Address}), nil
	}

	// The channel is not defined, nor is it defined by the user. Return error.
	return &(Channel{}), errors.New("Did not find specified channel name")
}

func getEpgData(numEpg int) {
	// We'll use the DB, so ensure it is up.
	ensureDbhConnection()

	for i, _ := range *(config.Channels) {
		arr := *(config.Channels)
		arr[i].EPGlist = []EPG{}
		rows, err := dbh.Query(`SELECT title, start, stop, description
                            FROM epg
                            WHERE channel=$1
                            AND stop > now()
                            LIMIT $2`, arr[i].Name, numEpg)
		if err != nil {
			logMessage("warn", "Could not fetch EPG-data from DB", err)
			return
		}
		for rows.Next() {
			var title, description string
			var start, stop time.Time
			err = rows.Scan(&title, &start, &stop, &description)
			if err != nil {
				logMessage("error", "Could not get EPG-data from DB", err)
				return
			}
			short_form := "15:04"
			long_form := "2006-01-02 15:04"
			epg := EPG{
				Title:       title,
				Start:       start.Format(short_form),
				Stop:        stop.Format(short_form),
				StartLong:   start.Format(long_form),
				StopLong:    stop.Format(long_form),
				Description: description,
			}
			arr[i].EPGlist = append(arr[i].EPGlist, epg)
		}
	}
}

func stopRecordingHandler(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		logMessage("error", "Could not convert recording-id to int", err)
		http.Redirect(w, &(r.Request), config.BaseUrl, 302)
	}

	// Remove the recording from the database
	err = removeRecording(int64(id))
	if err != nil {
		logMessage("error", "Could not remove recording", err)
		http.Redirect(w, &(r.Request), config.BaseUrl, 302)
	}

	if _, ok := recordings[int64(id)]; ok {
		_ = killStream(recordings[int64(id)].Cmd)
	}
	http.Redirect(w, &(r.Request), config.BaseUrl, 302)
}

func startRecording(sstart, sstop, username, title, channel, transcode string) {
	// Parse the time.
	layout := "2006-01-02 15:04"
	short_layout := "15:04"
	file_layout := "2006-01-02-15-04"
	start, err := time.Parse(layout, sstart)
	stop, err := time.Parse(layout, sstop)
	if err != nil {
		fmt.Println(err.Error())
		return
	}

	// Go assumes UTC, and as we are on Go 1.0 and not 1.1 we dont have ParseInLocation.
	// Thus we just convert now-time to UTC and add appropriate hours, based on local time zone.
	_, inFuture := time.Now().Zone()
	oslo := time.Now().UTC().Add(time.Duration(time.Duration(inFuture) * time.Second))

	duration := stop.Sub(start).Seconds()
	secondsInFuture := start.Sub(oslo).Seconds()
	if secondsInFuture < 0 {
		// Programme has already started.
		duration = stop.Sub(oslo).Seconds()
	}

	if duration < 0 {
		logMessage("error", "Starting recording failed due to negative duration", err)
		return
	}

	// Add the recording to the array of recordings for this user.
	programme_title := strings.Replace(title, " ", "-", -1)
	filename := fmt.Sprintf("%v/%v-%v-%v.mkv", config.RecordingsFolder, time.Now().Format(file_layout), programme_title, username)
	id, err := insertRecording(username, title, channel, transcode, start, stop)
	if err != nil {
		logMessage("error", "Could not insert recording", err)
		return
	}

	// Get the channel for this recording
	ch, err := getChannel(channel, username)
	if err != nil {
		logMessage("error", "Could not get channel in order to start recording", err)
		return
	}
	command := getVLCstr(0, ch.Address, filename, "file")
	cmd := exec.Command("bash", "-c", command)
	recordings[id] = Recording{
		Id:          id,
		User:        username,
		Title:       programme_title,
		Start:       start.Format(layout),
		Stop:        stop.Format(short_layout),
		Channel:     channel,
		Transcoding: transcode,
		Cmd:         cmd,
	}

	if !(secondsInFuture <= 0) {
		// Wait until programme starts.
		time.Sleep(time.Duration(int(secondsInFuture)) * time.Second)
	}

	// Start the recording and save to disk.
	err = cmd.Start()
	if err != nil {
		fmt.Println(err.Error())
		return
	}

	// Wait until programme stops.
	time.Sleep(time.Duration(int(duration)) * time.Second)

	// Kill the recording.
	err = killStream(cmd)
	if err != nil {
		logMessage("error", "Could not kill recording", err)
	}

	err = removeRecording(id)
	if err != nil {
		logMessage("error", "Could not remove recording", err)
	}
}

func startRecordingHandler(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
	start := r.FormValue("start")
	stop := r.FormValue("stop")
	title := r.FormValue("title")
	channel := r.FormValue("channel")
	transcode := r.FormValue("transcode")
	user, _ := getUserFromRequest(r)

	go startRecording(start, stop, user.Name, title, channel, transcode)
	http.Redirect(w, &r.Request, config.BaseUrl, 302)
}

func startVlcHandler(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
	t, err := template.ParseFiles("templates/vlc.html")
	if err != nil {
		logMessage("error", "Could not parse template file for VLC-player", err)
		return
	}
	d := make(map[string]interface{})
	d["Url"] = r.FormValue("url")
	d["BaseUrl"] = config.BaseUrl
	t.Execute(w, d)
}

func deleteRecording(name string) error {
	return os.Remove(config.RecordingsFolder + "/" + name)
}

func insertSubscription(title string, weekday int, interval []int, channel string, username string) error {
	// We'll use the DB, so ensure it is up.
	ensureDbhConnection()

	// Insert the subscription.
	tx, err := dbh.Begin()
	_, err = tx.Exec(`INSERT INTO subscriptions(
	title,interval_start,interval_stop,weekday,channel,username) VALUES
	($1,$2,$3,$4,$5,$6)`,
		title, interval[0], interval[1], weekday, channel, username)
	if err != nil { return err }
	tx.Commit()

	return nil
}

func addHoursToInt(h int, d int) int {
	// Calculate the time-interval
	dur := time.Duration(time.Duration(d) * time.Hour)
	// We use a base-time, in order to elegantly calculate e.g. 23:00 + 2 hours = 01:00.
	baseTime := time.Date(1970, 1, 1, h, 0, 0, 0, time.UTC)
	return baseTime.Add(dur).Hour()
}

func startSeriesSubscription(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
	// Get parameters from form
	title := r.FormValue("title")
	channel := r.FormValue("channel")
	weekday, err := strconv.Atoi(r.FormValue("weekday"))
	t, err := strconv.Atoi(r.FormValue("time"))
	if err != nil {
		logMessage("error", "Could not parse subscription time", err)
	}

	interval := []int{addHoursToInt(t, -config.SubIntervalSize), addHoursToInt(t, config.SubIntervalSize)}

	// Insert the subscription
	err = insertSubscription(title, weekday, interval, channel, r.Username)
	if err != nil {
		logMessage("error", "Could not insert the subscription", err)
	}

	// And check if we should start a recording right away.
	err = checkSubscriptions()
	if err != nil {
		logMessage("error", "Could not check and refresh the subscriptions", err)
	}

	// Redirect to front-page.
	http.Redirect(w, &(r.Request), config.BaseUrl, 302)
}

func removeSubscriptionHandler(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
	// Parse GET-parameters
	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		logMessage("error", "Could not convert subscription id to int64", err)
	}

	// Delete the subscription
	err = removeSubscription(r.Username, int64(id))
	if err != nil {
		logMessage("error", "Could not delete the subscription", err)
	}

	// Redirect to front-page.
	http.Redirect(w, &(r.Request), config.BaseUrl, 302)
}

func removeSubscription(username string, id int64) error {
	// We'll use the DB, so ensure it is up.
	ensureDbhConnection()

	// Check that the username owns this recording.
	dbh.QueryRow("SELECT * FROM subscriptions WHERE username = $1 AND id = $2", username, id)
	if sql.ErrNoRows == nil {
		return errors.New("A user tried to delete a subscription he/she did not own")
	}

	tx, err := dbh.Begin()
	if err != nil { return err }

	_, err = tx.Exec("DELETE FROM subscriptions WHERE id = $1", id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func checkSubscriptions() error {
	// We'll use the DB, so ensure it is up.
	ensureDbhConnection()

	// Time stamp used in the recording thread.
	long_form := "2006-01-02 15:04"

	// A nice query, finding all subscriptions not already in recordings.
	s := config.SubIntervalSize
	stmt := fmt.Sprintf(`SELECT epg.start, epg.stop, epg.title, epg.channel, subscriptions.username
											FROM epg
											JOIN subscriptions ON epg.title = subscriptions.title
											WHERE (subscriptions.title, subscriptions.channel) NOT IN (
											SELECT title, channel FROM recordings
											)
											AND epg.start::time - '%d hours'::interval >= (to_char(subscriptions.interval_start, '09') || ':00')::time
											AND epg.start::time + '%d hours'::interval >= (to_char(subscriptions.interval_stop, '09') || ':00')::time
											AND extract(dow from epg.start) = subscriptions.weekday
											AND epg.channel = subscriptions.channel`, s, s)
	rows, err := dbh.Query(stmt)
	if err != nil { return err }
	for rows.Next() {
		var title, channel, username string
		var start, stop time.Time
		rows.Scan(&start, &stop, &title, &channel, &username)

		// Start the recording, and for now default to 0 transcoding.
		go startRecording(start.Format(long_form), stop.Format(long_form), username, title, channel, "0")
	}

	return nil
}

func checkSubscriptionsHandler(w http.ResponseWriter, r *http.Request) {
	err := checkSubscriptions()
	if err != nil {
		logMessage("error", "Could not refresh and check subscriptions", err)
	}
	// This is accessed by clients, as an API (or whatever) -- so just output something.
	fmt.Fprintf(w, "Ok.")
}

func getSeriesSubscriptions(username string) ([]Subscription, error) {
	// We'll use the DB, so ensure it is up.
	ensureDbhConnection()

	// Get all subs for this user.
	rows, err := dbh.Query("SELECT id, title, interval_start, interval_stop, weekday, channel FROM subscriptions WHERE username = $1", username)
	if err != nil {
		logMessage("warn", "Getting titles failed", err)
	}

	var subs []Subscription
	for rows.Next() {
		var title, channel string
		var id, interval_start, interval_stop, weekday int
		rows.Scan(&id, &title, &interval_start, &interval_stop, &weekday, &channel)

		// Get the zero-padded starttime
		stime := zeroPad(strconv.Itoa(addHoursToInt(interval_start, config.SubIntervalSize)))

		// Gives more sense that something happens at 00, compared to 24.
		if stime == "24" {
			stime = "00"
		}

		// Translate the weekdays to Norwegian.
		weekday_nor := getNorwegianWeekday(weekday)

		// Add the subscription to the array of subscriptions.
		subs = append(subs, Subscription{
			Id:        int64(id),
			Title:     title,
			StartTime: stime,
			Weekday:   weekday_nor,
			Channel:   channel,
		})
	}

	return subs, nil
}

func getAllPrograms() ([]string, error) {
	// We'll use the DB, so ensure it is up.
	ensureDbhConnection()

	// Array holding the program titles.
	var programs []string

	// Select all existing programs
	rows, err := dbh.Query("SELECT DISTINCT title FROM epg ORDER BY title")
	if err != nil {
		return programs, err
	}

	// Add each title to the array.
	for rows.Next() {
		var title string
		_ = rows.Scan(&title)
		programs = append(programs, title)
	}
	return programs, nil

}

func archivePageHandler(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
	t, err := template.ParseFiles("templates/archive.html")
	if err != nil {
		logMessage("error", "Could not parse template file for archive", err)
		return
	}

	// Check if we requested to delete a file.
	deleteform := r.FormValue("delete")
	if deleteform != "" {
		err := deleteRecording(deleteform)
		if err != nil {
			logMessage("error", "Could not delete recording", err)
			return
		}

		// File deleted, redirect back to archive.
		base_url := fmt.Sprintf("%varchive", config.BaseUrl)
		http.Redirect(w, &r.Request, base_url, 302)
	}

	// Ensure the recordings-folder exists.
	if _, err := os.Stat(config.RecordingsFolder); err != nil {
		err := os.Mkdir(config.RecordingsFolder, 0755)
		if (err != nil) {
			logMessage("error", "Could not create recordingsfolder", err)
		}
	}

	// Get all recordings in the archive folder.
	recordings, err := ioutil.ReadDir(config.RecordingsFolder)

	// Make an empty file.
	fs := make([]File, 0)

	baseurl := "http://" + r.Host + r.URL.Path + "?url="
	for _, file := range recordings {
		streamurl := "http://" + r.Host + config.BaseUrl + config.RecordingsFolder + "/" + file.Name()
		fileurl := baseurl + streamurl
		// Add the file to array and display MB.
		fs = append(fs, File{Name: file.Name(), Size: (file.Size() / 1000000), Url: fileurl, SUrl: streamurl})
	}

	// Map holding our parameters.
	d := make(map[string]interface{})
	d["Files"] = fs
	d["BaseUrl"] = config.BaseUrl
	if err != nil {
		logMessage("error", "Could not list archive", err)
		return
	}
	t.Execute(w, d)
}

func startChannel(ch Channel, u User, transcoding int) error {
	// First kill current running channel, if any.
	err := killUniStream(u)
	if err != nil {
		return err
	}

	// Check if we want to access with http or cubemap
	access := "http"
	if config.CubemapConfig != "" {
		access += "{metacube}"
	}

	// And start the new specified channel.
	cmd, err := startUniStream(ch, u, transcoding, access)
	if err != nil {
		return err
	}

	// Add the new stream to as the "current running stream" for this user.
	streams[u.Name] = Command{
		Name:      ch.Name,
		Cmd:       cmd,
		Transcode: transcoding,
		Address:   ch.Address,
	}

	// Write cubemap-config, this is ignored if config.CubemapConfig is empty.
	// That is, it's ignored if we don't have Cubemap enabled.
	err = writeCubemapConfig(config.CubemapConfig)
	if err != nil {
		logMessage("error", "Could not update cubemap-config", err)
	}

	return nil
}

func addChannelHandler(w http.ResponseWriter, r *http.Request) {
	// Add a channel, and if it already exist we edit the URL.
	cname := r.FormValue("cname")
	url := r.FormValue("url")
	if cname == "" || url == "" {
		logMessage("error", "Recieved malformed parameters to addChannel", nil)
		fmt.Fprintf(w, "Error: malformed parameters.")
		return
	}

	// Pass empty username, as we are only interested in config-channels.
	channel, notfound := getChannel(cname, "")
	if notfound != nil {
		// The channel was not found, add it.
		*(config.Channels) = append(*(config.Channels), Channel{Name: cname, Address: url})
	} else {
		// Channel was found, edit the url.
		channel.Address = url
	}
	fmt.Fprintf(w, "Ok.")
}

func startExternalStream(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
	user, err := getUserFromRequest(r)
	if err != nil {
		logMessage("error", "Authentication problem", err)
		http.Redirect(w, &r.Request, config.BaseUrl, 302)
	}

	// Construct a custom channel, for this purpose
	n := r.FormValue("name")
	if n == "" { n = "Egendefinert kanal" }

	// Get the transcoding, defaulting to 0.
	transcoding := getTranscoding(r.FormValue("transcoding"))

	s := Channel{
		Name:    n,
		Address: r.FormValue("url"),
	}

	err = startChannel(s, user, transcoding)
	if err != nil {
		logMessage("error", "Could not start external stream", err)
	}

	http.Redirect(w, &r.Request, config.BaseUrl, 302)
}

func uniPageHandler(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
	// Show running channel and list of channels.
	t, err := template.ParseFiles("templates/index.html")
	if err != nil {
		logMessage("error", "Could not parse template file", err)
		return
	}

	// Ensure user that has logged in, is in the system.
	user, err := getUserFromRequest(r)
	if err != nil {
		logMessage("error", "Authentication problem", err)
		return
	}

	// Check if we already are playing a channel.
	currentChannel := ""

	// Check if we pass a channel name in parameters
	channelName := r.FormValue("channel")

	// Check if we want to transcode the stream.
	ftranscoding := r.FormValue("transcoding")
	transcoding := getTranscoding(ftranscoding)
	currentTranscoding := 0

	if _, ok := streams[user.Name]; ok {
		currentChannel = streams[user.Name].Name
		currentTranscoding = streams[user.Name].Transcode
	}

	// Get number of elements to show in the EPG feed
	form_epg := r.FormValue("num")
	numEpg := 3
	numEpg, _ = strconv.Atoi(form_epg)

	getEpgData(numEpg)

	// Check that the form-values are non empty and that they are different from
	// current configuration. If true, we kill stream and start a new one.
	if (channelName != "" && channelName != currentChannel) || (ftranscoding != "" && transcoding != currentTranscoding) {
		// First, get channel struct we want to change to.
		channel, err := getChannel(channelName, user.Name)
		if err != nil {
			logMessage("error", "Could not get channel", err)
			return
		}

		// Then kill existing stream and start the one chosen.
		err = startChannel(*channel, user, transcoding)
		if err != nil {
			logMessage("error", "Could not change channel", err)
		}

		// Easiest now is just to redirect the user back to the index.
		http.Redirect(w, &(r.Request), config.BaseUrl, 302)
	}

	// Check if requested to kill the channel, currently running..
	kill_index := r.FormValue("kchannel")
	if kill_index != "" {
		err := killUniStream(user)
		if err != nil {
			logMessage("error", "Could not kill stream", err)
		}
		http.Redirect(w, &(r.Request), config.BaseUrl, 302)
	}

	// Get number of viewers on current channel
	currentViewers := ""
	if _, ok := streams[user.Name]; ok {
		currentViewers = countStream(streams[user.Name].Cmd.Process.Pid, user.Id)
	}

	subscriptions, err := getSeriesSubscriptions(user.Name)
	if err != nil {
		logMessage("warn", "Could not get subscriptions", err)
	}

	// Get all program titles from EPG-data
	programs, err := getAllPrograms()
	if err != nil {
		logMessage("error", "Could not get alle programs from DB", err)
	}

	// Get the URL for this user.
	userURL := fmt.Sprintf("http://%v:%v%v/%v", config.Hostname, config.StreamingPort, user.Id, user.Name)
	if config.CubemapConfig != "" {
		userURL = fmt.Sprintf("http://%s:%d/%s", config.Hostname, config.CubemapPort, user.Name)
	}

	// Get the recordings for this user.
	d := make(map[string]interface{})
	d["Recordings"] = recordings
	d["RecordingsFolder"] = config.RecordingsFolder
	d["Viewers"] = currentViewers
	d["Channels"] = config.Channels
	d["BaseUrl"] = config.BaseUrl
	d["User"] = user.Name
	d["CurrentChannel"] = currentChannel
	d["CurrentAddress"] = streams[user.Name].Address
	d["Transcoding"] = currentTranscoding
	d["Subscriptions"] = subscriptions
	d["Programs"] = programs
	d["URL"] = userURL
	d["Running"] = (currentChannel != "")

	t.Execute(w, d)
}

func countStream(pid int, userid string) string {
	cmd := fmt.Sprintf("lsof -a -p %d -i tcp:%v%v | grep ESTABLISHED | wc -l", pid, config.StreamingPort, userid)
	oneliner := exec.Command("bash", "-c", cmd)
	out, _ := oneliner.Output()
	return strings.TrimSpace(string(out))
}

func getPid(serviceName string) (int, error) {
	// Ask bash for the PID.
	spid, err := exec.Command("bash", "-c", "pidof cubemap|head -n 1").Output()

	// Check if service is running.
	if len(spid) == 0 {
		return -1, errors.New("Service not found")
	}

	// Remove last new line.
	spid = spid[:len(spid)-1]

	// Convert to int and return
	pid, err := strconv.Atoi(string(spid))
	if err != nil {
		return -1, err
	}
	return pid, nil
}

func writeCubemapConfig(filename string) error {
	if filename == "" {
		// The filename is empty. So we will not update the config.
		// Nor will we return an error, we just ignore everything.
		return nil
	}

	// Write a config file for cubemap
	d := "num_servers 1\n"
	d += "port 9094\n"
	d += "stats_file cubemap.stats\n"
	d += "stats_interval 60\n"
	d += "input_stats_file cubemap-input.stats\n"
	d += "input_stats_interval 60\n"
	d += "access_log access.log\n"
	d += "error_log type=file filename=cubemap.log\n"
	d += "error_log type=syslog\n"
	d += "error_log type=console\n"

	// Add all streams to the config-file.
	for username, _ := range streams {
		u, err := getUserFromName(username)
		if err != nil {
			return err
		}

		d += fmt.Sprintf("stream /%s src=http://%s:%s%s/%s encoding=metacube", u.Name, config.Hostname, config.StreamingPort, u.Id, u.Name)
	}

	// Write the config file
	err := ioutil.WriteFile(filename, []byte(d), 0644)
	if err != nil {
		return err
	}
	logMessage("info", fmt.Sprintf("Wrote new cubemap-config to %s", filename), nil)

	// SIGHUP the cubemap service
	pid, err := getPid("cubemap")
	if err != nil {
		return errors.New(fmt.Sprintf("Could not send SIGHUP to cubemap, %s", err.Error()))
	}
	err = syscall.Kill(pid, syscall.SIGHUP)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	var cubemap = flag.String("cubemap", "", "Use cubemap as a VLC-reflector")
	flag.Parse()

	// First thing to do, read the configuration file.
	config = loadConfig("config.json")

	// Create the DBH
	ensureDbhConnection()

	// Check if we want to use cubemap as reflector
	if *cubemap != "" {
		config.CubemapConfig = *cubemap
		err := writeCubemapConfig(*cubemap)
		if err != nil {
			logMessage("info", "Got error re-execing cubemap", err)
		}
		for _, err := getPid("cubemap"); err != nil; {
			logMessage("info", "Cubemap service not running. Waiting 2 seconds before continuing", nil)
			time.Sleep(2 * time.Second)
		}
	}

	// The server has (re)started, so we load in the planned recordings.
	err := loadPlannedRecordings()
	if err != nil {
		logMessage("error", "Failed to initialize recordings", err)
	}

	// Defining our paths
	secrets := auth.HtpasswdFileProvider(config.PasswordFile)
	authenticator := auth.NewBasicAuthenticator(config.Hostname, secrets)
	http.HandleFunc("/", authenticator.Wrap(uniPageHandler))
	http.HandleFunc("/external", authenticator.Wrap(startExternalStream))
	http.HandleFunc("/record", authenticator.Wrap(startRecordingHandler))
	http.HandleFunc("/stopRecording", authenticator.Wrap(stopRecordingHandler))
	http.HandleFunc("/vlc", authenticator.Wrap(startVlcHandler))
	http.HandleFunc("/archive", authenticator.Wrap(archivePageHandler))
	http.HandleFunc("/startSubscription", authenticator.Wrap(startSeriesSubscription))
	http.HandleFunc("/deleteSubscription", authenticator.Wrap(removeSubscriptionHandler))

	// No auth
	http.HandleFunc("/checkSubscriptions", checkSubscriptionsHandler)
	http.HandleFunc("/addChannel", addChannelHandler)

	// Static content, including video-files of old recordings.
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static/"))))
	http.Handle("/"+config.RecordingsFolder+"/", http.FileServer(http.Dir("")))

	if config.Debug {
		logMessage("debug", "Serverer nettsiden på http://localhost:"+config.WebPort, nil)
	}
	err = http.ListenAndServe(":"+config.WebPort, nil)
	if err != nil {
		logMessage("error", "Problemer med å serve content", err)
	}
}
