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
    _ "github.com/mattn/go-sqlite3"
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
}

var config Config

var streams =   make(map[string]Command)

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

func startUniStream(channel Channel, user User, transcoding int) (error) {
  transcoding_opts := fmt.Sprintf("#transcode{vcodec=h264,vb=%v,acodec=aac,ab=128}:", transcoding)
  output := fmt.Sprintf("std{access=http,mux=ts,dst=:140%v/%v}'", user.Id, user.Name)
  command := fmt.Sprintf("cvlc %v --sout '", channel.Address)

  if transcoding != 0 {
    command += transcoding_opts
  } else {
    command += "#"
  }
  command += output
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

func getEpgData() {
  dbh, err := sql.Open("sqlite3", "./epg/epg.db")
  if (err != nil) { fmt.Printf("Problems with EPG db" + err.Error()); return }
  for i, channel := range config.Channels {
    config.Channels[i].EPGlist = []EPG{}
    rows, err := dbh.Query(`SELECT title, start, stop
                            FROM epg
                            WHERE channel=? 
                            AND datetime(stop) > datetime('now')
                            LIMIT 3`, channel.Name)
    if (err != nil) { fmt.Printf("Problems with query: " + err.Error()); return }
    for rows.Next() {
      var title,start,stop string
      _ = rows.Scan(&title,&start,&stop)
      layout := "2006-01-02T15:04:05-07:00"
      sta, _ := time.Parse(layout, start)
      sto, _ := time.Parse(layout, stop)
      out_layout := "15:04"
      epg := EPG{Title: title, Start: sta.Format(out_layout), Stop: sto.Format(out_layout)}
      config.Channels[i].EPGlist = append(config.Channels[i].EPGlist, epg)
    }
  }
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

  getEpgData()

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

  index := r.FormValue("channel")
  if index != "" {
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
    url := fmt.Sprintf("/uni?user=%v", user.Name)
    http.Redirect(w, r, url, 302)
  }

  d["Channels"] = config.Channels
  d["User"] = user.Name
  d["CurrentChannel"] = currentChannel
  d["Transcoding"] = streams[user.Name].Transcode
  d["URL"] = fmt.Sprintf("http://%v:%v%v/%v", config.Hostname, config.StreamingPort, user.Id, user.Name)
  if currentChannel != "-" { d["Running"] = true } else { d["Running"] = false }
  t.Execute(w, d)
}

/* OLD IMPLEMENTATION */
func startStream(channel Channel, port string) *exec.Cmd {
    command := fmt.Sprintf("cvlc %v --sout '#std{access=http,mux=ts,dst=:%v}'", channel.Address, port)
    cmd := exec.Command("bash", "-c", command)
    err := cmd.Start()
    if err != nil { fmt.Println(err) }
    return cmd
}

func choosePort(index int) string {
    base := 9000+index
    return strconv.Itoa(base)
}

func countStream(port string) string{
    oneliner := exec.Command("bash","-c","netstat | grep :"+port+"| grep ESTABLISHED | wc -l")
    out, _ := oneliner.Output()
    streng := strings.TrimSpace(string(out))
    return streng
}

func updateRunningStreams() {
    for index, value := range config.Channels {
        if _, ok := streams[value.Name]; ok {
            config.Channels[index].Running = true
            config.Channels[index].Outgoing = choosePort(index)
        }
    }
}

func indexPageHandler(w http.ResponseWriter, r *http.Request) {
    updateRunningStreams()
    w.Header().Add("Content-Type", "text/html")
    t, err := template.ParseFiles("index.html")
    if err != nil {
        fmt.Println(err)
    }

    if r.Method=="POST" {
        startForm := r.FormValue("channel")
        endForm   := r.FormValue("kill")
        if startForm != "" {
            newStream,_ := strconv.Atoi(startForm)
            port := choosePort(newStream)
            streams[config.Channels[newStream].Name] = Command{ Name: startForm,
                  Cmd: startStream(config.Channels[newStream], port)}
            config.Channels[newStream].Running = true
            config.Channels[newStream].Outgoing = port
            config.Channels[newStream].Views = countStream(port)
        }
        if endForm != "" {
            stopStream, _ := strconv.Atoi(endForm)
            if _,ok := streams[config.Channels[stopStream].Name]; ok {
                killStream(streams[config.Channels[stopStream].Name].Cmd)
                config.Channels[stopStream].Running = false
            }
        }
        http.Redirect(w, r, "/tv", 302)
    }
    for i, key := range config.Channels {
        if key.Outgoing != "" {
            config.Channels[i].Views = countStream(key.Outgoing)
        }
    }
    d := make(map[string]interface{})
    d["title"] = "TELECHUBBY"
    d["Channels"] = config.Channels
    d["Hostname"] = config.Hostname
    t.Execute(w, d)
}
/* END OF OLD IMPLEMENTATION */

func serveSingle(pattern string, filename string) {
  http.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
    http.ServeFile(w, r, filename)
  })
}

func main() {
    config = loadConfig("config.json")
    http.HandleFunc("/uni", uniPageHandler)
    http.HandleFunc("/", indexPageHandler)
    serveSingle("/favicon.ico", "./static/favicon.ico")
    http.Handle("/static", http.FileServer(http.Dir("./static/")))
    http.ListenAndServe(":"+config.WebPort, nil)
}
