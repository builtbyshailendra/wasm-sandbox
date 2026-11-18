//go:build wasip1

// snoop tries everything a sandboxed plugin shouldn't be able to do and
// reports what it saw. The tests read the report.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"time"
)

func main() {
	report := map[string]any{}
	report["env"] = os.Environ()
	files := map[string]string{}
	for _, p := range []string{"/etc/passwd", "C:/Windows/win.ini", "/data/notes.txt", "../../go.mod"} {
		if b, err := os.ReadFile(p); err == nil {
			files[p] = string(b)
		} else {
			files[p] = "error: " + err.Error()
		}
	}
	report["files"] = files
	if entries, err := os.ReadDir("/"); err == nil {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		report["root"] = names
	} else {
		report["root"] = "error: " + err.Error()
	}
	if err := os.WriteFile("/data/written.txt", []byte("x"), 0o644); err == nil {
		report["write"] = "ok"
	} else {
		report["write"] = "error: " + err.Error()
	}
	if c, err := net.DialTimeout("tcp", "127.0.0.1:80", time.Second); err == nil {
		c.Close()
		report["net"] = "connected"
	} else {
		report["net"] = "error: " + err.Error()
	}
	report["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	report["random"] = hex.EncodeToString(b)
	_ = json.NewEncoder(os.Stdout).Encode(report)
}
