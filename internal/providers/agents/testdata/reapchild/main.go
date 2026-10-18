//go:build !windows

// Command reapchild is the test-only process the lease-sweep tests spawn. It
// inherits its environment untouched (so it carries whatever CASCADE_LEASE_ID
// the test set) and then behaves as its first argument says:
//
//	sleep <pidfile>    write own pid to pidfile, then sleep until killed
//	fork  <pidfile>    start a "sleep" copy of itself in a new session
//	                   (setsid), then exit, leaving that copy an orphan
//	scrub <pidfile>    like sleep, but re-exec with an empty environment first
//	print-env <name>   print the value of one environment variable and exit
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "sleep":
		writePid(os.Args[2])
		time.Sleep(10 * time.Minute)
	case "fork":
		cmd := exec.Command(os.Args[0], "sleep", os.Args[2])
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		_ = cmd.Process.Release()
	case "scrub":
		cmd := exec.Command(os.Args[0], "sleep", os.Args[2])
		cmd.Env = []string{}
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		_ = cmd.Process.Release()
	case "print-env":
		fmt.Println(os.Getenv(os.Args[2]))
	default:
		os.Exit(2)
	}
}

// writePid records this process's pid atomically, so a poller never reads a
// partial number.
func writePid(path string) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(4)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Exit(4)
	}
}
