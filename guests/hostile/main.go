//go:build wasip1

// hostile misbehaves in the way stdin asks: {"mode": "loop" | "alloc" |
// "spew" | "panic" | "exit3"}.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

var keep [][]byte

func main() {
	var in struct {
		Mode string `json:"mode"`
	}
	_ = json.NewDecoder(os.Stdin).Decode(&in)
	switch in.Mode {
	case "loop":
		n := 0
		for {
			n++ // never ends
		}
	case "alloc":
		for {
			keep = append(keep, make([]byte, 1<<20)) // 1 MiB at a time, forever
		}
	case "spew":
		line := strings.Repeat("x", 1023) + "\n"
		for {
			fmt.Print(line)
		}
	case "panic":
		panic("plugin bug")
	case "exit3":
		fmt.Fprintln(os.Stderr, "giving up")
		os.Exit(3)
	}
	fmt.Println(`{"ok":true}`)
}
