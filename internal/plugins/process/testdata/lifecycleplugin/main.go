// Command lifecycleplugin is a test-only process plugin that the
// internal/plugins/process tests build into t.TempDir(). It speaks the
// host handshake and then misbehaves on request, so Close, the lifetime
// and the closed environment are proven against a real child.
//
// Flags:
//
//	-dir DIR          write DIR/env-<pid> (this process's environment, one
//	                  entry per line) at startup
//	-ignore-term      ignore SIGTERM
//	-ignore-eof       keep running after stdin reaches EOF
//	-grandchild       fork a sleeper grandchild that inherits stdout,
//	                  ignores SIGTERM and carries a planted token; its pid
//	                  goes to DIR/grandchild.pid
//	-crash-first M    if file M is absent, create it and exit 3 right
//	                  after answering the handshake (forces one respawn)
//	-sleeper          be the grandchild: ignore SIGTERM and sleep
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	dir := flag.String("dir", "", "")
	ignoreTerm := flag.Bool("ignore-term", false, "")
	ignoreEOF := flag.Bool("ignore-eof", false, "")
	grandchild := flag.Bool("grandchild", false, "")
	crashFirst := flag.String("crash-first", "", "")
	sleeper := flag.Bool("sleeper", false, "")
	flag.Parse()

	if *sleeper {
		signal.Ignore(syscall.SIGTERM)
		must(os.WriteFile(filepath.Join(*dir, "grandchild.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600))
		time.Sleep(time.Hour)
		return
	}
	if *ignoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	if *dir != "" {
		name := filepath.Join(*dir, "env-"+strconv.Itoa(os.Getpid()))
		must(os.WriteFile(name, []byte(strings.Join(os.Environ(), "\n")), 0o600))
	}
	if *grandchild {
		forkSleeper(*dir)
	}
	crash := false
	if *crashFirst != "" {
		if _, err := os.Stat(*crashFirst); os.IsNotExist(err) {
			must(os.WriteFile(*crashFirst, nil, 0o600))
			crash = true
		}
	}
	serve(crash)
	if *ignoreEOF {
		time.Sleep(time.Hour)
	}
}

// serve answers every request until stdin reaches EOF. The first request
// is the handshake; crash exits right after answering it.
func serve(crash bool) {
	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var req struct {
			ID     *uint64 `json:"id"`
			Method string  `json:"method"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		result := map[string]any{"ok": true}
		if req.Method == "cascade.hello" {
			result = map[string]any{"protocol_version": "1.0.0", "manifest_hash": "lifecycle"}
		}
		must(out.Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result}))
		if crash {
			os.Exit(3)
		}
	}
}

// forkSleeper starts this binary again as the grandchild. It inherits
// stdout (so the plugin's stdout stays open after the plugin dies) and the
// plugin's environment plus a planted token.
func forkSleeper(dir string) {
	self, err := os.Executable()
	must(err)
	cmd := exec.Command(self, "-sleeper", "-dir", dir)
	cmd.Stdout = os.Stdout
	cmd.Env = append(os.Environ(), "PLANTED_TOKEN=lifecycle-"+"planted-0001")
	must(cmd.Start())
}

func must(err error) {
	if err != nil {
		os.Stderr.WriteString("lifecycleplugin: " + err.Error() + "\n")
		os.Exit(2)
	}
}
