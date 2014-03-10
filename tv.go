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

var channels    []Channel
var streams =   make(map[string]*exec.Cmd)
var baseUrl =   "http://hoftun.mg.am:"
var port        string

func loadConfig(filename string) []Channel {
    file, err := ioutil.ReadFile(filename)
    if err != nil {
        fmt.Println("Problemer med å lese config-fil: ", err)
    }
    err = json.Unmarshal(file, &channels)
    if err != nil {
        fmt.Println("Problemer med å pakke ut config: ", err)
    }
    return channels
}

func startStream(channel Channel, port string) *exec.Cmd {
    cmd := exec.Command("/usr/bin/cvlc", channel.Address,
            "--sout","#std{access=http,mux=ts,dst=:"+port+"}")
    err := cmd.Start()
    if err != nil {
        fmt.Println(err)
    }
    return cmd
}

func killStream(cmd *exec.Cmd) {
    if err := cmd.Process.Kill(); err != nil {
        fmt.Println(err)
    }
    cmd.Process.Wait()
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
    for index, value := range channels {
        if _, ok := streams[value.Name]; ok {
            channels[index].Running = true
            channels[index].Outgoing = baseUrl+strconv.Itoa(index)
        }
    }
}
            
func indexPageHandler(w http.ResponseWriter, r *http.Request) {
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
            port = choosePort(newStream)
            streams[channels[newStream].Name] = 
                    startStream(channels[newStream], port)
            channels[newStream].Running = true
            channels[newStream].Outgoing = port
            channels[newStream].Views = countStream(port) 
        }
        if endForm != "" {
            stopStream, _ := strconv.Atoi(endForm)
            if _,ok := streams[channels[stopStream].Name]; ok {
                killStream(streams[channels[stopStream].Name])
                channels[stopStream].Running = false
            }
        }
        http.Redirect(w, r, "/tv", 302)
    }
    for i, key := range channels {
        if key.Outgoing != "" {
            channels[i].Views = countStream(key.Outgoing)
        }
    }
    data := TemplateData{Title: "TELECHUBBY", Channels: channels,}
    t.Execute(w, data)
}

func main() {
    channels = loadConfig("channels.json")
    updateRunningStreams()
    http.HandleFunc("/", indexPageHandler)
    http.ListenAndServe(":13000", nil)

}
