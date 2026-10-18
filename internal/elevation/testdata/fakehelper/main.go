// Purpose: a test-only stand-in for cascade-elevate-helper. It speaks the
//
//	helper protocol with a fixed, public, obviously fake Ed25519 key held in
//	memory (no keychain, no real key material), so the cascade-side keystore
//	tests can exercise the real exec path: fd 3 is the gate that names
//	cascade's listener, and the request/reply travel on the connection the
//	helper makes to it.
//
// Inputs: fd 3 (one line: the listener path); a sidecar file "mode" beside
//
//	this binary picks the behaviour: ok (default), sleep (never answers),
//	flood (streams bytes without a newline until killed), prewrite (writes a
//	forged line into the gate before it is asked), keeper (starts a
//	descendant that connects to the listener first and answers forged data).
//
// Outputs: one response line on the connection; side effects beside the
//
//	binary: an "execs" file gains one byte per start, a "pid" file holds the
//	process id, a "keeperpid" file the descendant's, and a "request" file
//	holds the request line once it was read (absent when the child was
//	killed first).
//
// Constraints: test-only; nothing here is built into a release artefact.
//
// SPORT: internal/elevation testdata fakehelper/ADDED (P1-SHIP-15).
package main

import (
	"bufio"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// fakeSeed is a public test value, not a secret.
var fakeSeed = []byte("cascade-fake-helper-test-seed-00")

func main() {
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	if len(os.Args) == 3 && os.Args[1] == "keeper" {
		keeper(dir, os.Args[2])
		return
	}
	recordStart(dir)
	mode := "ok"
	if b, err := os.ReadFile(filepath.Join(dir, "mode")); err == nil {
		mode = strings.TrimSpace(string(b))
	}
	gate := os.NewFile(3, "gate")
	if mode == "prewrite" {
		// A swapped-in program that wrote a forged line before becoming this
		// (genuine) image under the same pid.
		_, _ = gate.Write([]byte("/forged/path\n"))
	}
	path, err := bufio.NewReader(gate).ReadString('\n')
	if err != nil {
		os.Exit(1)
	}
	path = strings.TrimSpace(path)
	if mode == "keeper" {
		cmd := exec.Command(exe, "keeper", path)
		cmd.Env = []string{}
		if cmd.Start() != nil {
			os.Exit(1)
		}
		time.Sleep(500 * time.Millisecond) // let the descendant connect first
	}
	serve(dir, mode, path)
}

// recordStart writes the sidecar files that say the helper started.
func recordStart(dir string) {
	if f, err := os.OpenFile(filepath.Join(dir, "execs"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_, _ = f.Write([]byte{'x'})
		_ = f.Close()
	}
	_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "envcount"), []byte(strconv.Itoa(len(os.Environ()))), 0o600)
}

// serve connects to the listener, reads one request and acts per mode.
func serve(dir, mode, path string) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		os.Exit(1)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		os.Exit(1)
	}
	_ = os.WriteFile(filepath.Join(dir, "request"), line, 0o600)
	switch mode {
	case "sleep":
		time.Sleep(time.Hour)
	case "flood":
		chunk := make([]byte, 1<<20)
		for {
			if _, err := conn.Write(chunk); err != nil {
				os.Exit(1)
			}
		}
	}
	reply(conn, line, "")
}

// reply answers one request. forgedPub, when set, replaces the public key.
func reply(conn net.Conn, line []byte, forgedPub string) {
	var req struct {
		Op         string `json:"op"`
		PayloadB64 string `json:"payload_b64"`
	}
	_ = json.Unmarshal(line, &req)
	priv := ed25519.NewKeyFromSeed(fakeSeed)
	resp := map[string]any{"v": 1, "ok": true}
	switch req.Op {
	case "status":
		resp["available"], resp["tier"], resp["cgo"] = true, "os-keychain", true
	case "pubkey":
		resp["pub_b64"] = base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
		if forgedPub != "" {
			resp["pub_b64"] = forgedPub
		}
	case "sign":
		payload, _ := base64.StdEncoding.DecodeString(req.PayloadB64)
		resp["sig_b64"] = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	}
	out, _ := json.Marshal(resp)
	_, _ = conn.Write(append(out, '\n'))
}

// keeper is a descendant of the helper that connects to the listener before
// the helper does and answers a forged public key; it then stays alive.
func keeper(dir, path string) {
	_ = os.WriteFile(filepath.Join(dir, "keeperpid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	if conn, err := net.Dial("unix", path); err == nil {
		if line, err := bufio.NewReader(conn).ReadBytes('\n'); err == nil {
			reply(conn, line, base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
		}
	}
	time.Sleep(time.Hour)
}
