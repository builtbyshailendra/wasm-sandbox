//go:build wasip1

// wordcount is a well-behaved plugin: JSON in on stdin, JSON out on stdout.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		fmt.Fprintln(os.Stderr, "bad input:", err)
		os.Exit(2)
	}
	freq := map[string]int{}
	words := strings.Fields(in.Text)
	for _, w := range words {
		if w = strings.ToLower(strings.Trim(w, `.,!?;:"'()`)); w != "" {
			freq[w]++
		}
	}
	type wc struct {
		Word  string `json:"word"`
		Count int    `json:"count"`
	}
	var top []wc
	for w, c := range freq {
		top = append(top, wc{w, c})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Count != top[j].Count {
			return top[i].Count > top[j].Count
		}
		return top[i].Word < top[j].Word
	})
	if len(top) > 3 {
		top = top[:3]
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"words": len(words), "lines": strings.Count(in.Text, "\n") + 1, "top": top})
}
