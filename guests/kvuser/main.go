//go:build wasip1

// kvuser reads keys from a key-value store the host provides. It can only
// work if the host grants the "kv" capability: the import below is checked
// before the plugin is allowed to load.
package main

import (
	"encoding/json"
	"os"
	"unsafe"
)

//go:wasmimport sandbox kv_get
func kvGet(keyPtr unsafe.Pointer, keyLen uint32, outPtr unsafe.Pointer, outCap uint32) uint32

const notFound = 0xFFFFFFFF

func get(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	k := []byte(key)
	buf := make([]byte, 4096)
	n := kvGet(unsafe.Pointer(&k[0]), uint32(len(k)), unsafe.Pointer(&buf[0]), uint32(len(buf)))
	if n == notFound || n > uint32(len(buf)) {
		return "", false
	}
	return string(buf[:n]), true
}

func main() {
	var in struct {
		Keys []string `json:"keys"`
	}
	_ = json.NewDecoder(os.Stdin).Decode(&in)
	out := map[string]any{}
	for _, k := range in.Keys {
		if v, ok := get(k); ok {
			out[k] = v
		} else {
			out[k] = nil
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}
