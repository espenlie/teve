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
)

type Channel struct {
    Name        string
    Address     string
    Running     bool
    Outgoing    string
    Views       string
}

type TemplateData struct {
    Title       string
    Channels    []Channel
}

type User struct {
  Name string
  Id string
}

type Config struct {
  Channels []Channel
  Users []User
  Domain string
}

type Command struct {
  Name string
  Cmd *exec.Cmd
  Transcode bool
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

func startUniStream(channel Channel, user User, transcoding bool) (error) {
  transcoding_opts := "#transcode{vcodec=h264,vb=2000,acodec=aac,ab=128}:"
  output := fmt.Sprintf("std{access=http,mux=ts,dst=:140%v/%v}'", user.Id, user.Name)
  command := fmt.Sprintf("cvlc %v --sout '", channel.Address)

  if transcoding {
    command += transcoding_opts
  } else {
    command += "#"
  }
  command += output
  fmt.Println(command)
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

func startStream(channel Channel, port string, user User) *exec.Cmd {
    cmd := exec.Command("/usr/bin/cvlc", channel.Address,
            "--sout","#std{access=http,mux=ts,dst=:1200" + user.Id + "/" + user.Name + "}")
    err := cmd.Start()
    if err != nil {
        fmt.Println(err)
    }
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
            config.Channels[index].Outgoing = config.Domain+strconv.Itoa(index)
        }
    }
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

func uniPageHandler(w http.ResponseWriter, r *http.Request) {
  // Ensure user has logged in.
  user, err := getUser(r.FormValue("user"))
  if (err != nil) { fmt.Fprintf(w, "You need to login: " + err.Error()); return }

  currentChannel := "-"

  transcoding := false
  if _, ok := streams[user.Name]; ok {
    currentChannel = streams[user.Name].Name
    transcoding = streams[user.Name].Transcode
  }

  // Check if we want to transcode the stream.
  form_transcoding := r.FormValue("transcoding")
  if form_transcoding == "true" {
    transcoding = true
  } else if form_transcoding == "false" {
    transcoding = false
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

  // Show running channel and list of channels.
  t, err := template.ParseFiles("channels.html")
  if (err != nil) { fmt.Fprintf(w, "Could not parse template file: " + err.Error()); return }

  d := make(map[string]interface{})
  d["Channels"] = config.Channels
  d["User"] = user.Name
  d["CurrentChannel"] = currentChannel
  d["Transcoding"] = transcoding
  if currentChannel != "-" { d["Running"] = true } else { d["Running"] = false }
  t.Execute(w, d)
}

func indexPageHandler(w http.ResponseWriter, r *http.Request) {
    _ = r.FormValue("user")
    var current_user User


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
                  Cmd: startStream(config.Channels[newStream], port, current_user)}
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
    data := TemplateData{Title: "TELECHUBBY", Channels: config.Channels,}
    t.Execute(w, data)
}

func main() {
    config = loadConfig("channels.json")
    updateRunningStreams()
    http.HandleFunc("/", indexPageHandler)
    http.HandleFunc("/uni", uniPageHandler)
    http.ListenAndServe(":13000", nil)

}
