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
	Users            []User
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
}

type Command struct {
	Name      string
	Cmd       *exec.Cmd
	Transcode int
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

func loadPlannedRecordings() {
	dboptions := fmt.Sprintf("host=%v dbname=%v user= %v password=%v sslmode=disable", config.DBHost, config.DBName, config.DBUser, config.DBPass)
	dbh, err := sql.Open("postgres", dboptions)
	if err != nil {
		fmt.Printf("Problems with recording db: %v\n", err.Error())
		return
	}
	long_form := "2006-01-02 15:04"

	// First delete those that have finished since last time.
	rows, err := dbh.Query("SELECT id FROM recordings WHERE stop < now()")
	if err != nil {
		fmt.Printf("Query failed: %v\n", err.Error())
		return
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		removeRecording(id)
	}

	rows, err = dbh.Query("SELECT start,stop,username,title,channel,transcode FROM recordings")
	if err != nil {
		fmt.Printf("Query failed: %v\n", err.Error())
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
	fmt.Printf("Loaded %d recordings from DB.\n", cnt)
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
		fmt.Printf("Problems with recordings db: %v\n", err.Error())
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
	streams[user.Name] = Command{Name: channel.Name, Cmd: cmd, Transcode: transcoding}
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

func getUser(username string) (User, error) {
	for _, user := range config.Users {
		if user.Name == username {
			return user, nil
		}
	}
	return User{}, errors.New("Did not find user.")
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
		fmt.Printf("Problems with EPG db" + err.Error())
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
			fmt.Printf("Problems with query: " + err.Error())
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
	username := r.FormValue("username")
	removeRecording(int64(id))
	if _, ok := recordings[int64(id)]; ok {
		_ = killStream(recordings[int64(id)].Cmd)
	}
	base_url := fmt.Sprintf("%vuri?user=%v&refresh=1", config.BaseUrl, username)
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

	secondsInFuture := start.Sub(oslo).Seconds()
	duration := stop.Sub(start).Seconds()

	if duration < 0 {
		fmt.Println("Failed due to negative duration.")
		return
	}

	// Add the recording to the array of recordings for this user.
	programme_title := strings.Replace(title, " ", "-", -1)
	filename := fmt.Sprintf("%v/%v-%v-%v.mkv", config.RecordingsFolder, time.Now().Format(file_layout), programme_title, username)
	id, err := insertRecording(username, title, channel, transcode, start, stop)
	if err != nil {
		fmt.Printf("Could not insert: %v\n", err.Error())
		return
	}
	ch, err := getChannel(channel)
	if err != nil {
		fmt.Println(err.Error())
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
	username := r.FormValue("user")
	start := r.FormValue("start")
	stop := r.FormValue("stop")
	title := r.FormValue("title")
	channel := r.FormValue("channel")
	transcode := r.FormValue("transcode")
	user, _ := getUser(username)

	//url := fmt.Sprintf("http://%v:%v%v/%v", config.Hostname, config.StreamingPort, user.Id, user.Name)
	go startRecording(start, stop, user.Name, title, channel, transcode)
	base_url := fmt.Sprintf("%vuri?user=%v&refresh=1", config.BaseUrl, username)
	http.Redirect(w, r, base_url, 302)
}

func startVlcHandler(w http.ResponseWriter, r *http.Request) {
	t, err := template.ParseFiles("vlc.html")
	if err != nil {
		fmt.Fprintf(w, "Could not parse template file: "+err.Error())
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
		fmt.Fprintf(w, "Could not parse template file: "+err.Error())
		return
	}
	deleteform := r.FormValue("delete")
	if deleteform != "" {
		err := deleteRecording(deleteform)
		if err != nil {
			fmt.Fprintf(w, "Could not delete recording: "+err.Error())
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
		fileurl := fmt.Sprintf("%vhttp://%v:%v@%v%vrecordings/%v", baseurl, config.HttpUser, config.HttpPass, config.Hostname, config.BaseUrl, file.Name())
		streamurl := fmt.Sprintf("http://%v%vrecordings/%v", config.Hostname, config.BaseUrl, file.Name())
		fs = append(fs, File{Name: file.Name(), Size: (file.Size() / 1000000), Url: fileurl, SUrl: streamurl})
	}
	d["Files"] = fs
	d["BaseUrl"] = config.BaseUrl
	if err != nil {
		fmt.Fprintf(w, "Could not list archive: "+err.Error())
		return
	}
	t.Execute(w, d)
}

func uniPageHandler(w http.ResponseWriter, r *http.Request) {
	// Show running channel and list of channels.
	d := make(map[string]interface{})
	t, err := template.ParseFiles("unistream.html")
	if err != nil {
		fmt.Fprintf(w, "Could not parse template file: "+err.Error())
		return
	}

	// Ensure user has logged in.
	user, err := getUser(r.FormValue("user"))
	if err != nil {
		d["Users"] = config.Users
		t.Execute(w, d)
		return
	}

	currentChannel := "-"

	transcoding := 0
	if _, ok := streams[user.Name]; ok {
		currentChannel = streams[user.Name].Name
	}

	// Check if we want to transcode the stream.
	form_transcoding := r.FormValue("transcoding")
	if form_transcoding != "0" {
		transcoding, err = strconv.Atoi(form_transcoding)
		if err != nil {
			transcoding = 0
		}
	}

	// Check if we actually want to kill and start a new stream, or just want to
	// refresh.
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

	index := r.FormValue("channel")
	if index != "" && !refresh {
		// Change the current channel
		// First, get channel.
		channel, err := getChannel(index)
		if err != nil {
			fmt.Fprintf(w, "Could not switch channel: "+err.Error())
			return
		}

		// Then kill current running channel, if any.
		err = killUniStream(user)
		if err != nil {
			fmt.Fprintf(w, "Could not kill stream: "+err.Error())
			return
		}

		// And start the new specified channel.
		err = startUniStream(channel, user, transcoding)
		if err != nil {
			fmt.Fprintf(w, "Could not start stream: "+err.Error())
			return
		}
		currentChannel = channel.Name
	}

	kill_index := r.FormValue("kchannel")
	if kill_index != "" {
		err := killUniStream(user)
		if err != nil {
			fmt.Fprintf(w, "Could not kill stream: "+err.Error())
			return
		}
		url := fmt.Sprintf("%vuni?user=%v", config.BaseUrl, user.Name)
		http.Redirect(w, r, url, 302)
	}
	// Get the recordings for this user.
	d["Recordings"] = recordings
	d["RecordingsFolder"] = config.RecordingsFolder
	d["Viewers"] = countStream()
	d["Channels"] = config.Channels
	d["BaseUrl"] = config.BaseUrl
	d["User"] = user.Name
	d["CurrentChannel"] = currentChannel
	d["Transcoding"] = streams[user.Name].Transcode
	d["URL"] = fmt.Sprintf("http://%v:%v%v/%v", config.Hostname, config.StreamingPort, user.Id, user.Name)
	if currentChannel != "-" {
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
	http.HandleFunc("/", uniPageHandler)
	http.HandleFunc("/record", startRecordingHandler)
	http.HandleFunc("/stopRecording", stopRecordingHandler)
	http.HandleFunc("/vlc", startVlcHandler)
	http.HandleFunc("/archive", archivePageHandler)
	serveSingle("/favicon.ico", "./static/favicon.ico")
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static/"))))
	http.Handle("/"+config.RecordingsFolder+"/", http.FileServer(http.Dir("")))
	http.ListenAndServe(":"+config.WebPort, nil)
}
