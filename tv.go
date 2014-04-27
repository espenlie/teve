package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	auth "github.com/abbot/go-http-auth"
	_ "github.com/lib/pq"
	"html/template"
	"io/ioutil"
	"net/http"
	"os"
	"log"
	"os/exec"
	"strconv"
	"strings"
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
}

type Command struct {
	Name      string
	Cmd       *exec.Cmd
	Transcode int
	Address   string
}

type EPG struct {
	Title     string
	Start     string
	Stop      string
	StartLong string
	StopLong  string
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

func loadConfig(filename string) Config {
	file, err := ioutil.ReadFile(filename)
	if err != nil {
		fmt.Println("Problemer med å lese config-fil: ", err)
	}
	err = json.Unmarshal(file, &config)
	if err != nil {
		fmt.Println("Problemer med å pakke ut config: ", err)
	}
	return config
}

func logMessage(level, msg string, err error) {
	// Uniform level strings
	level = strings.ToUpper(level)

	// Get the error message
	e := ""
	if err != nil { e = ":" + err.Error() }

	// Quit the program if we get an error
	if (level == "ERROR") {
		log.Fatalf("[%s] %s %s\n", level, msg, e)
	}

	// Else we just print the messgae to stdout.
	log.Printf("[%s] %s %s\n", level, msg, e)
}

func getTranscoding(trans string) int {
	var err error
	transcoding := 0
	if trans != "0" {
		transcoding, err = strconv.Atoi(trans)
		if err != nil {
			transcoding = 0
		}
	}
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

func loadPlannedRecordings() {
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		logMessage("warn", "Cant connect to the PostgreSQL-DB at "+config.DBHost, err)
		return
	}
	long_form := "2006-01-02 15:04"

	// First delete those that have finished since last time.
	rows, err := dbh.Query("SELECT id FROM recordings WHERE stop < now()")
	if err != nil {
		logMessage("warn", "Getting old finished recordings failed", err)
		return
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		err := removeRecording(id)
		if err != nil {
			logMessage("error", "Could not remove recording", err)
		}
	}

	rows, err = dbh.Query("SELECT start,stop,username,title,channel,transcode FROM recordings")
	if err != nil {
		logMessage("warn", "Getting future recordings failed", err)
		return
	}
	cnt := 0
	for rows.Next() {
		var username, title, channel, transcode string
		var start, stop time.Time
		rows.Scan(&start, &stop, &username, &title, &channel, &transcode)
		go startRecording(start.Format(long_form), stop.Format(long_form), username, title, channel, transcode)
		cnt += 1
	}
	logMessage("info", fmt.Sprintf("Loaded %d recordings from DB", cnt), nil)
}

func insertRecording(username, title, channel, transcode string, start, stop time.Time) (int64, error) {
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		return -1, err
	}
	tx, err := dbh.Begin()
	id := int64(-1)
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
			return -1, err
		}
	} else if err != nil {
		// There was an actual DB-error.
		return -1, err
	}
	// We have either inserted the recording successfully, or the recording
	// already exists and we return the id.
	return id, nil
}

func removeRecording(id int64) error {
	// Delete from the DB
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		return err
	}
	tx, _ := dbh.Begin()
	_, err = tx.Exec("DELETE FROM recordings WHERE id = $1", id)
	if err != nil {
		return err
	}
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

func startUniStream(channel Channel, user User, transcoding int) error {
	userSuffix := fmt.Sprintf(":%v%v/%v", config.StreamingPort, user.Id, user.Name)
	command := getVLCstr(transcoding, channel.Address, userSuffix, "http")
	cmd := exec.Command("bash", "-c", command)
	err := cmd.Start()
	if err != nil {
		return err
	}
	streams[user.Name] = Command{
		Name:      channel.Name,
		Cmd:       cmd,
		Transcode: transcoding,
		Address:   channel.Address,
	}
	return nil
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
	if len(n) == 1 {
		return fmt.Sprintf("0%v", n)
	}
	return n
}

func getUser(r *auth.AuthenticatedRequest) (User, error) {
	username := r.Username
	f, err := ioutil.ReadFile(".htpasswd")
	if err != nil {
		return User{}, err
	}
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
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		logMessage("error", "Could not connect to EPG-db", err)
		return
	}
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
			if (err != nil) {
				logMessage("error", "Could not get EPG-data from DB", err)
				return
			}
			short_form := "15:04"
			long_form := "2006-01-02 15:04"
			epg := EPG{
				Title:     title,
				Start:     start.Format(short_form),
				Stop:      stop.Format(short_form),
				StartLong: start.Format(long_form),
				StopLong:  stop.Format(long_form),
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
	user, _ := getUser(r)

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
	err := os.Remove(config.RecordingsFolder + "/" + name)
	if err != nil {
		return err
	}
	return nil
}

func insertSubscription(title string, weekday int, interval []int, channel string, username string) error {
	// Connect to DB
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		return err
	}

	// Insert the subscription.
	tx, err := dbh.Begin()
	_, err = tx.Exec(`INSERT INTO subscriptions(
	title,interval_start,interval_stop,weekday,channel,username) VALUES
	($1,$2,$3,$4,$5,$6)`,
		title, interval[0], interval[1], weekday, channel, username)
	if err != nil {
		return err
	}
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
	// Connect to the DB
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		return err
	}

	// Check that the username owns this recording.
	dbh.QueryRow("SELECT * FROM subscriptions WHERE username = $1 AND id = $2", username, id)
	if sql.ErrNoRows == nil {
		return errors.New("A user tried to delete a subscription he/she did not own")
	}

	tx, err := dbh.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec("DELETE FROM subscriptions WHERE id = $1", id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func checkSubscriptions() error {
	// Connect to the DB
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		return err
	}

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
	if err != nil {
		return err
	}
	for rows.Next() {
		var title, channel, username string
		var start, stop time.Time
		rows.Scan(&start, &stop, &title, &channel, &username)
		go startRecording(start.Format(long_form), stop.Format(long_form), username, title, channel, "0")
	}

	return nil
}

func checkSubscriptionsHandler(w http.ResponseWriter, r *http.Request) {
	err := checkSubscriptions()
	if err != nil {
		logMessage("error", "Could not refresh and check subscriptions", err)
	}
	fmt.Fprintf(w, "Ok.")
}

func getSeriesSubscriptions(username string) ([]Subscription, error) {
	// Connect to DB
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		return nil, err
	}

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
	var programs []string

	// Connect to DB
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		return programs, err
	}

	// Select all existing programs
	rows, err := dbh.Query("SELECT DISTINCT title FROM epg ORDER BY title")
	if err != nil {
		return programs, err
	}

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
	deleteform := r.FormValue("delete")
	if deleteform != "" {
		err := deleteRecording(deleteform)
		if err != nil {
			logMessage("error", "Could not delete recording", err)
			return
		}
		base_url := fmt.Sprintf("%varchive", config.BaseUrl)
		http.Redirect(w, &r.Request, base_url, 302)
	}
	d := make(map[string]interface{})
	recordings, err := ioutil.ReadDir(config.RecordingsFolder)
	fs := make([]File, 0)
	baseurl := fmt.Sprintf("http://%v%vvlc?url=", config.Hostname, config.BaseUrl)
	for _, file := range recordings {
		fileurl := fmt.Sprintf("%vhttp://%v%vrecordings/%v", baseurl, config.Hostname, config.BaseUrl, file.Name())
		streamurl := fmt.Sprintf("http://%v%vrecordings/%v", config.Hostname, config.BaseUrl, file.Name())
		fs = append(fs, File{Name: file.Name(), Size: (file.Size() / 1000000), Url: fileurl, SUrl: streamurl})
	}
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

	// And start the new specified channel.
	err = startUniStream(ch, u, transcoding)
	if err != nil {
		return err
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
	user, err := getUser(r)
	if err != nil {
		logMessage("error", "Authentication problem", err)
		http.Redirect(w, &r.Request, config.BaseUrl, 302)
	}

	// Construct a custom channel, for this purpose
	n := r.FormValue("name")
	if n == "" {
		n = "Egendefinert kanal"
	}

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
	d := make(map[string]interface{})
	t, err := template.ParseFiles("templates/index.html")
	if err != nil {
		logMessage("error", "Could not parse template file", err)
		return
	}

	// Ensure user that has logged in, is in the system.
	user, err := getUser(r)
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
	numEpg := 3
	form_epg := r.FormValue("num")
	if form_epg != "" {
		numEpg, err = strconv.Atoi(form_epg)
		if err != nil {
			numEpg = 3
		}
	}
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

	kill_index := r.FormValue("kchannel")
	if kill_index != "" {
		err := killUniStream(user)
		if err != nil {
			logMessage("error", "Could not kill stream", err)
			return
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

	// Get the recordings for this user.
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
	d["URL"] = fmt.Sprintf("http://%v:%v%v/%v", config.Hostname, config.StreamingPort, user.Id, user.Name)
	if currentChannel != "" {
		d["Running"] = true
	} else {
		d["Running"] = false
	}
	t.Execute(w, d)
}

func countStream(pid int, userid string) string {
	cmd := fmt.Sprintf("lsof -a -p %d -i tcp:%v%v | grep ESTABLISHED | wc -l", pid, config.StreamingPort, userid)
	oneliner := exec.Command("bash", "-c", cmd)
	out, _ := oneliner.Output()
	return strings.TrimSpace(string(out))
}

func main() {
	config = loadConfig("config.json")

	// The server has (re)started, so we load in the planned recordings.
	loadPlannedRecordings()

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
	err := http.ListenAndServe(":"+config.WebPort, nil)
	if err != nil {
		logMessage("error", "Problemer med å serve content", err)
	}
}
