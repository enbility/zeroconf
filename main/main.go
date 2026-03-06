package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/enbility/zeroconf/v3"
)


func main() {
	server, err := zeroconf.Register("GoZeroconf", "_workstation._tcp", "local", 42424, []string{"txtv=0", "lo=1", "la=2"}, nil)
	if err != nil {
		fmt.Println(err.Error())
	}
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	_ = <-sigChan
	server.Shutdown()
}