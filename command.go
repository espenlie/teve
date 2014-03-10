package main

import (
    "fmt"
    "os/exec"
    "strings"
)


func cmd(port string) string{
    netstat := exec.Command("netstat")
    grep1   := exec.Command("grep",":"+port)
    grep2   := exec.Command("grep","ESTABLISHED")
    wc      := exec.Command("wc","-l")
    out1, _ := netstat.StdoutPipe()
    netstat.Start()
    grep1.Stdin = out1
    out2, _ := grep1.StdoutPipe()
    grep1.Start()
    grep2.Stdin = out2
    out3, _ := grep2.StdoutPipe()
    grep2.Start()
    wc.Stdin = out3
    output, _ := wc.Output()
    return string(output)
}

func main() {
    command := exec.Command("bash", "-c",`ip a | wc -l`)
    hei,_:=command.Output()

    fmt.Println(strings.TrimSpace(string(hei)))
}
