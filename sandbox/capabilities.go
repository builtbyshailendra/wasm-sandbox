package sandbox

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Capabilities a host can grant, by the import that asks for them.
// A plugin declares what it needs simply by importing it; the host decides
// at load time, before any plugin code runs.
var capabilities = map[string]string{
	"sandbox.kv_get": "kv",
}

// checkImports lists the capabilities a module imports and refuses it if it
// imports anything ungranted or unknown.
func checkImports(name string, m wazero.CompiledModule, g Grants) ([]string, error) {
	seen := map[string]bool{}
	for _, f := range m.ImportedFunctions() {
		mod, fn, _ := f.Import()
		if mod == wasi_snapshot_preview1.ModuleName {
			continue // WASI itself; what it can reach is decided by the module config
		}
		capName, known := capabilities[mod+"."+fn]
		if !known {
			return nil, fmt.Errorf("%s imports %s.%s, which this host doesn't provide", name, mod, fn)
		}
		if !granted(capName, g) {
			return nil, fmt.Errorf("%s needs the %q capability (it imports %s.%s), which wasn't granted", name, capName, mod, fn)
		}
		seen[capName] = true
	}
	needs := make([]string, 0, len(seen))
	for c := range seen {
		needs = append(needs, c)
	}
	sort.Strings(needs)
	return needs, nil
}

func granted(capName string, g Grants) bool {
	switch capName {
	case "kv":
		return g.KV != nil
	}
	return false
}

type kvCounter struct{ n atomic.Int64 }

const kvNotFound = 0xFFFFFFFF

// instantiateKV provides sandbox.kv_get(keyPtr, keyLen, outPtr, outCap) -> len.
//
// The guest passes pointers into its own linear memory. The host reads and
// writes only through api.Memory, which bounds-checks every access, so a
// bad pointer gets "not found" rather than touching host memory. A value
// longer than outCap returns its length without being written, so the
// guest can retry with a bigger buffer.
func instantiateKV(ctx context.Context, rt wazero.Runtime, kv map[string]string, count *kvCounter) error {
	data := make(map[string]string, len(kv)) // a copy: the caller can't change it under a running plugin
	for k, v := range kv {
		data[k] = v
	}
	get := func(_ context.Context, m api.Module, keyPtr, keyLen, outPtr, outCap uint32) uint32 {
		count.n.Add(1)
		key, ok := m.Memory().Read(keyPtr, keyLen)
		if !ok {
			return kvNotFound
		}
		val, ok := data[string(key)]
		if !ok {
			return kvNotFound
		}
		if uint32(len(val)) > outCap {
			return uint32(len(val))
		}
		if !m.Memory().Write(outPtr, []byte(val)) {
			return kvNotFound
		}
		return uint32(len(val))
	}
	_, err := rt.NewHostModuleBuilder("sandbox").
		NewFunctionBuilder().WithFunc(get).Export("kv_get").
		Instantiate(ctx)
	return err
}
