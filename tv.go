package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/lib/pq"
	"html/template"
	"io/ioutil"
	"net/http"
	"os"
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
	Channels         []Channel
	Hostname         string
	HttpUser         string
	HttpPass         string
	BaseUrl          string
	StreamingPort    string
	WebPort          string
	RecordingsFolder string
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
	e := ""
	if err != nil {
		e = ": " + err.Error()
	}
	fmt.Printf("[%v] %v%v\n", strings.ToUpper(level), msg, e)
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
		removeRecording(id)
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
	var id int64
	err = tx.QueryRow(`SELECT id FROM recordings
                     WHERE title = $1
                     AND channel = $2
                     AND start = $3
                     AND stop = $4`, title, channel, start, stop).Scan(&id)
	if err == sql.ErrNoRows {
		// Great the recording does not exist in the DB yet, lets insert it.
		res, err := tx.Exec(`INSERT INTO recordings(
    start,stop,username,title,channel,transcode) VALUES
    ($1,$2,$3,$4,$5,$6)`,
			start, stop, username, title, channel, transcode)
		if err != nil {
			return -1, err
		}
		id, _ = res.LastInsertId()
		_ = tx.Commit()
	} else if err != nil {
		return -1, err
	}
	// We have either inserted the recording successfully, or the recording
	// already exists and we return the id.
	return id, nil
}

func removeRecording(id int64) {
	// Delete from the DB
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		logMessage("warn", "Could not connect to recordings-db", err)
		return
	}
	tx, _ := dbh.Begin()
	_, err = tx.Exec("DELETE FROM recordings WHERE id = $1", id)
	if err != nil {
		fmt.Printf("Could not remove: %v\n", err.Error())
		return
	}
	_ = tx.Commit()

	// Delete from cmd and kill command.
	delete(recordings, id)
}

func getVLCstr(transcoding int, channel, dst, access string) string {
	transcoding_opts := fmt.Sprintf("#transcode{vcodec=mp2v,vb=%v,acodec=aac,ab=128,scale=0.7,threads=2}:", transcoding)
	output := fmt.Sprintf("std{access=%v,mux=ts,dst=%v}'", access, dst)
	command := fmt.Sprintf("cvlc %v --sout '", channel)

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

func getUser(r *http.Request) (User, error) {
	if config.Debug {
		// In debug-mode we don't have Basic Auth, and thus return a placeholder user.
		logMessage("debug", "Defaulting to default user knuta, as we are in debug mode", nil)
		return User{Name: "knuta", Id: "01"}, nil
	}

	username := r.Header.Get("X-Remote-User")
	f, err := ioutil.ReadFile(".htpasswd")
	if err != nil {
		return User{}, err
	}
	lines := strings.Split(string(f), "\n")
	for id, line := range lines[0 : len(lines)-1] {
		s := strings.SplitN(line, ":", 2)
		strId := strconv.Itoa(id)
		if username == s[0] {
			if len(strId) == 1 {
				strId = fmt.Sprintf("0%v", strId)
			}
			return User{Name: username, Id: strId}, nil
		}
	}
	return User{}, errors.New("Did not find user '" + username + "' authenticated from Basic Auth.")
}

func getChannel(channel_name string) (Channel, error) {
	for _, channel := range config.Channels {
		if channel.Name == channel_name {
			return channel, nil
		}
	}
	return Channel{}, errors.New("Did not find specified channel name")
}

func getEpgData(numEpg int) {
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		logMessage("error", "Could not connect to EPG-db", err)
		return
	}
	for i, channel := range config.Channels {
		config.Channels[i].EPGlist = []EPG{}
		rows, err := dbh.Query(`SELECT title, start, stop
                            FROM epg
                            WHERE channel=$1
                            AND stop > now()
                            LIMIT $2`, channel.Name, numEpg)
		if err != nil {
			logMessage("warn", "Could not fetch EPG-data from DB", err)
			return
		}
		for rows.Next() {
			var title string
			var start, stop time.Time
			_ = rows.Scan(&title, &start, &stop)
			short_form := "15:04"
			long_form := "2006-01-02 15:04"
			epg := EPG{
				Title:     title,
				Start:     start.Format(short_form),
				Stop:      stop.Format(short_form),
				StartLong: start.Format(long_form),
				StopLong:  stop.Format(long_form),
			}
			config.Channels[i].EPGlist = append(config.Channels[i].EPGlist, epg)
		}
	}
}

func stopRecordingHandler(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.FormValue("id"))
	removeRecording(int64(id))
	if _, ok := recordings[int64(id)]; ok {
		_ = killStream(recordings[int64(id)].Cmd)
	}
	base_url := fmt.Sprintf("%v?refresh=1", config.BaseUrl)
	http.Redirect(w, r, base_url, 302)
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
	ch, err := getChannel(channel)
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
		fmt.Println(err.Error())
		return
	}
	removeRecording(id)
}

func startRecordingHandler(w http.ResponseWriter, r *http.Request) {
	start := r.FormValue("start")
	stop := r.FormValue("stop")
	title := r.FormValue("title")
	channel := r.FormValue("channel")
	transcode := r.FormValue("transcode")
	user, _ := getUser(r)

	//url := fmt.Sprintf("http://%v:%v%v/%v", config.Hostname, config.StreamingPort, user.Id, user.Name)
	go startRecording(start, stop, user.Name, title, channel, transcode)
	base_url := fmt.Sprintf("%vuri?&refresh=1", config.BaseUrl)
	http.Redirect(w, r, base_url, 302)
}

func startVlcHandler(w http.ResponseWriter, r *http.Request) {
	t, err := template.ParseFiles("vlc.html")
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

func archivePageHandler(w http.ResponseWriter, r *http.Request) {
	t, err := template.ParseFiles("archive.html")
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
		http.Redirect(w, r, base_url, 302)
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

func startExternalStream(w http.ResponseWriter, r *http.Request) {
	user, err := getUser(r)
	if err != nil {
		logMessage("error", "Authentication problem", err)
		http.Redirect(w, r, config.BaseUrl, 302)
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

	http.Redirect(w, r, config.BaseUrl, 302)
}

func uniPageHandler(w http.ResponseWriter, r *http.Request) {
	// Show running channel and list of channels.
	d := make(map[string]interface{})
	t, err := template.ParseFiles("index.html")
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
	if _, ok := streams[user.Name]; ok {
		currentChannel = streams[user.Name].Name
	}

	// Check if we want to transcode the stream.
	transcoding := getTranscoding(r.FormValue("transcoding"))

	// Check if we want to refresh stream (change it)
	refresh := false
	form_refresh := r.FormValue("refresh")
	if form_refresh == "1" {
		refresh = true
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

	// If '&channel' is passed, it means we want to change/start a stream.
	channelName := r.FormValue("channel")
	if channelName != "" && refresh {
		// First, get channel struct we want to change to.
		channel, err := getChannel(channelName)
		if err != nil {
			logMessage("error", "Could not get channel", err)
			return
		}

		// Then kill existing stream and start the one chosen.
		err = startChannel(channel, user, transcoding)
		if err != nil {
			logMessage("error", "Could not change channel", err)
		}
		currentChannel = channel.Name
	}

	kill_index := r.FormValue("kchannel")
	if kill_index != "" {
		err := killUniStream(user)
		if err != nil {
			logMessage("error", "Could not kill stream", err)
			return
		}
		http.Redirect(w, r, config.BaseUrl, 302)
	}
	// Get the recordings for this user.
	d["Recordings"] = recordings
	d["RecordingsFolder"] = config.RecordingsFolder
	d["Viewers"] = countStream()
	d["Channels"] = config.Channels
	d["BaseUrl"] = config.BaseUrl
	d["User"] = user.Name
	d["CurrentChannel"] = currentChannel
	d["CurrentAddress"] = streams[user.Name].Address
	d["Transcoding"] = streams[user.Name].Transcode
	d["URL"] = fmt.Sprintf("http://%v:%v%v/%v", config.Hostname, config.StreamingPort, user.Id, user.Name)
	if currentChannel != "" {
		d["Running"] = true
	} else {
		d["Running"] = false
	}
	t.Execute(w, d)
}

func countStream() string {
	oneliner := exec.Command("bash", "-c", "netstat | grep :"+config.StreamingPort+"| grep ESTABLISHED | wc -l")
	out, _ := oneliner.Output()
	streng := strings.TrimSpace(string(out))
	return streng
}

func serveSingle(pattern string, filename string) {
	http.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filename)
	})
}

func main() {
	config = loadConfig("config.json")

	// The server has (re)started, so we load in the planned recordings.
	loadPlannedRecordings()

	// Defining our paths
	http.HandleFunc("/", uniPageHandler)
	http.HandleFunc("/external", startExternalStream)
	http.HandleFunc("/record", startRecordingHandler)
	http.HandleFunc("/stopRecording", stopRecordingHandler)
	http.HandleFunc("/vlc", startVlcHandler)
	http.HandleFunc("/archive", archivePageHandler)

	// Hack in order to serve the favicon without web-server
	serveSingle("/favicon.ico", "./static/favicon.ico")

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
