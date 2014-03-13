package main

import (
    "encoding/json"
    "fmt"
    "io/ioutil"
    "html/template"
    "net/http"
    "os/exec"
    "strconv"
    "strings"
    "errors"
    "time"
    "database/sql"
    _ "github.com/lib/pq"
)

type Channel struct {
    Name        string
    Address     string
    Running     bool
    Outgoing    string
    Views       string
    EPGlist     []EPG
}

type User struct {
  Name string
  Id string
}

type Config struct {
  Channels []Channel
  Users []User
  Hostname string
  StreamingPort string
  WebPort string
  RecordingsFolder string
  DBHost string
  DBName string
  DBUser string
  DBPass string
}

type Command struct {
  Name string
  Cmd *exec.Cmd
  Transcode int
}

type EPG struct {
  Title string
  Start string
  Stop string
  StartLong string
  StopLong string
}

type Recording struct {
  Id int
  Channel string
  Start string
  Stop string
  Title string
}

var config Config

var streams = make(map[string]Command)
var recordings = make(map[string][]Recording)

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

func startUniStream(channel Channel, user User, transcoding int) (error) {
  userSuffix := fmt.Sprintf(":140%v/%v", user.Id, user.Name)
  command := getVLCstr(transcoding, channel.Address, userSuffix, "http")
  cmd := exec.Command("bash", "-c", command)
  err := cmd.Start()
  if (err != nil) { return err }
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

func killStream(cmd *exec.Cmd) (error) {
    if err := cmd.Process.Kill(); err != nil { return err }
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
  if (err != nil) { fmt.Printf("Problems with EPG db" + err.Error()); return }
  for i, channel := range config.Channels {
    config.Channels[i].EPGlist = []EPG{}
    rows, err := dbh.Query(`SELECT title, start, stop
                            FROM epg
                            WHERE channel=$1
                            AND stop > now()
                            LIMIT $2`, channel.Name, numEpg)
    if (err != nil) { fmt.Printf("Problems with query: " + err.Error()); return }
    for rows.Next() {
      var title string
      var start,stop time.Time
      _ = rows.Scan(&title,&start,&stop)
      short_form := "15:04"
      long_form := "2006-01-02 15:04"
      epg := EPG{
        Title: title,
        Start: start.Format(short_form),
        Stop: stop.Format(short_form),
        StartLong: start.Format(long_form),
        StopLong: stop.Format(long_form),
      }
      config.Channels[i].EPGlist = append(config.Channels[i].EPGlist, epg)
    }
  }
}

func startRecording(url, sstart, sstop, username, title, channel string) {
  // Parse the time.
  layout := "2006-01-02 15:04"
  short_layout := "15:04"
  file_layout := "2006-01-02-15-04"
  start, err := time.Parse(layout, sstart)
  stop, err := time.Parse(layout, sstop)
  if (err != nil) { fmt.Println(err.Error()); return }
  // Go assumes UTC, and as we are on Go 1.0 and not 1.1 we dont have ParseInLocation.
  // Thus we just convert now-time to UTC and add an hour.
  oslo := time.Now().UTC().Add(time.Duration(3600*time.Second))

  secondsInFuture := start.Sub(oslo).Seconds()
  secondsToEnd := stop.Sub(oslo).Seconds()
  if (secondsToEnd < 0) {
    fmt.Println("Failed due to negative duration.")
    return
  }

  // Add the recording to the array of recordings for this user.
  recs := recordings[username]
  key := len(recs)
  programme_title := strings.Replace(title, " ", "-", -1)
  filename := fmt.Sprintf("%v/%v-%v-%v.mkv", config.RecordingsFolder, start.Format(file_layout), programme_title, username)
  recs = append(recs, Recording{
    Id: key,
    Channel: "-",
    Start: start.Format(layout),
    Stop: stop.Format(short_layout),
    Title: programme_title,
  })
  recordings[username] = recs

  if !(secondsInFuture < 0 && secondsToEnd > 0) {
    // Wait until programme starts.
    time.Sleep(time.Duration(int(secondsInFuture))*time.Second)
  }

  // Start the recording and save to disk.
  //command := fmt.Sprintf("ffmpeg -i %v -c copy %v.mkv", url, filename)
  ch, err := getChannel(channel)
  if (err != nil) { fmt.Println(err.Error()); return }
  command := getVLCstr(0, ch.Address, filename, "file")
  cmd := exec.Command("bash", "-c", command)
  err = cmd.Start()
  if (err != nil) { fmt.Println(err.Error()); return }

  // Wait until programme stops.
  time.Sleep(time.Duration(int(secondsToEnd))*time.Second)

  // Kill the recording.
  err = killStream(cmd)
  if (err != nil) { fmt.Println(err.Error()); return }

  // Remove the recording from the user.
  for i, recording := range recs {
    if recording.Id == key {
      recs = append(recs[:i],recs[i+1:]...)
    }
  }
  recordings[username] = recs
}

func startRecordingHandler(w http.ResponseWriter, r *http.Request) {
  username := r.FormValue("user")
  start := r.FormValue("start")
  stop := r.FormValue("stop")
  title := r.FormValue("title")
  channel := r.FormValue("channel")
  user, _ := getUser(username)

  url := fmt.Sprintf("http://%v:%v%v/%v", config.Hostname, config.StreamingPort, user.Id, user.Name)

  go startRecording(url, start, stop, user.Name, title, channel)
  base_url := fmt.Sprintf("/uri?user=%v&refresh=1", username)
  http.Redirect(w, r, base_url, 302)
}

func uniPageHandler(w http.ResponseWriter, r *http.Request) {
  // Show running channel and list of channels.
  d := make(map[string]interface{})
  t, err := template.ParseFiles("unistream.html")
  if (err != nil) { fmt.Fprintf(w, "Could not parse template file: " + err.Error()); return }

  // Ensure user has logged in.
  user, err := getUser(r.FormValue("user"))
  if (err != nil) {
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
    if (err != nil) { transcoding = 0 }
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
    if (err != nil) { numEpg = 3 }
  }
  getEpgData(numEpg)

  index := r.FormValue("channel")
  if index != "" && !refresh {
    // Change the current channel
    // First, get channel.
    channel, err := getChannel(index)
    if (err != nil) { fmt.Fprintf(w, "Could not switch channel: " + err.Error()); return }

    // Then kill current running channel, if any.
    err = killUniStream(user)
    if (err != nil) { fmt.Fprintf(w, "Could not kill stream: " + err.Error()); return }

    // And start the new specified channel.
    err = startUniStream(channel, user, transcoding)
    if (err != nil) { fmt.Fprintf(w, "Could not start stream: " + err.Error()); return }
    currentChannel = channel.Name
  }

  kill_index := r.FormValue("kchannel")
  if kill_index != "" {
    err := killUniStream(user)
    if (err != nil) { fmt.Fprintf(w, "Could not kill stream: " + err.Error()); return }
    url := fmt.Sprintf("/tv/uni?user=%v", user.Name)
    http.Redirect(w, r, url, 302)
  }

  // Get the recordings for this user.
  d["Recordings"] = recordings[user.Name]
  d["RecordingsFolder"] = config.RecordingsFolder
  d["Viewers"] = countStream()
  d["Channels"] = config.Channels
  d["User"] = user.Name
  d["CurrentChannel"] = currentChannel
  d["Transcoding"] = streams[user.Name].Transcode
  d["URL"] = fmt.Sprintf("http://%v:%v%v/%v", config.Hostname, config.StreamingPort, user.Id, user.Name)
  if currentChannel != "-" { d["Running"] = true } else { d["Running"] = false }
  t.Execute(w, d)
}

/* OLD IMPLEMENTATION */
// We could edit this to count all outgoing streams
func countStream() string{
    oneliner := exec.Command("bash","-c","netstat | grep :"+config.StreamingPort+"| grep ESTABLISHED | wc -l")
    out, _ := oneliner.Output()
    streng := strings.TrimSpace(string(out))
    return streng
}
/* END OF OLD IMPLEMENTATION */

func serveSingle(pattern string, filename string) {
  http.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
    http.ServeFile(w, r, filename)
  })
}

func main() {
    config = loadConfig("config.json")
    http.HandleFunc("/", uniPageHandler)
    http.HandleFunc("/record", startRecordingHandler)
    serveSingle("/favicon.ico", "./static/favicon.ico")
    http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static/"))))
    http.Handle("/"+config.RecordingsFolder+"/", http.FileServer(http.Dir("")))
    http.ListenAndServe(":"+config.WebPort, nil)
}
