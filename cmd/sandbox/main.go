// Command sandbox runs one WebAssembly plugin with limits.
//
//	sandbox run [flags] plugin.wasm < input.json
//
// Exit status: the plugin's own exit code, or 124 for a timeout, 125 for the
// memory or output limit, 126 for a trap, 2 for a usage or load error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"

	"github.com/builtbyshailendra/wasm-sandbox/sandbox"
)

type kvFlag map[string]string

func (k kvFlag) String() string { return fmt.Sprint(map[string]string(k)) }
func (k kvFlag) Set(s string) error {
	key, val, ok := strings.Cut(s, "=")
	if !ok || key == "" {
		return fmt.Errorf("want key=value, got %q", s)
	}
	k[key] = val
	return nil
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: sandbox run [flags] plugin.wasm < input.json")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	timeout := fs.Duration("timeout", 2*time.Second, "wall-clock limit")
	memory := fs.Uint("memory", 64, "memory limit in MiB")
	maxOut := fs.Int("max-output", 1<<20, "stdout+stderr limit in bytes")
	dataDir := fs.String("data", "", "host directory to mount read-only at /data")
	realClock := fs.Bool("real-clock", false, "give the plugin the real clock and real randomness")
	kv := kvFlag{}
	fs.Var(kv, "kv", "grant the kv capability with this entry (repeatable): -kv region=ap-south-1")
	defaultCache := filepath.Join(os.TempDir(), "wasm-sandbox-cache")
	if dir, err := os.UserCacheDir(); err == nil {
		defaultCache = filepath.Join(dir, "wasm-sandbox")
	}
	cacheDir := fs.String("cache-dir", defaultCache, "where compiled machine code is kept between runs (empty: none)")
	_ = fs.Parse(os.Args[2:])
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: sandbox run [flags] plugin.wasm < input.json")
		os.Exit(2)
	}

	ctx := context.Background()
	wasm, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		os.Exit(2)
	}
	var cache wazero.CompilationCache
	if *cacheDir != "" {
		if cache, err = wazero.NewCompilationCacheWithDir(*cacheDir); err != nil {
			fmt.Fprintln(os.Stderr, "sandbox: cache:", err)
			os.Exit(2)
		}
	}
	grants := sandbox.Grants{DataDir: *dataDir, RealClock: *realClock}
	if len(kv) > 0 {
		grants.KV = kv
	}
	loadStart := time.Now()
	p, err := sandbox.Load(ctx, filepath.Base(fs.Arg(0)), wasm,
		sandbox.Limits{Timeout: *timeout, MemoryMiB: uint32(*memory), MaxOutput: *maxOut}, grants, cache)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		os.Exit(2)
	}
	defer p.Close(ctx)
	loaded := time.Since(loadStart)

	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		os.Exit(2)
	}
	r := p.Run(ctx, input)
	os.Stdout.Write(r.Stdout)
	os.Stderr.Write(r.Stderr)
	outcome := string(r.Outcome)
	if r.Outcome == sandbox.Exited {
		outcome = fmt.Sprintf("exited %d", r.ExitCode) // the plugin's own code; limits use wazero's internal codes
	}
	fmt.Fprintf(os.Stderr, "sandbox: %s, load %s, run %s\n", outcome,
		loaded.Round(time.Millisecond), r.Duration.Round(time.Millisecond))
	switch r.Outcome {
	case sandbox.OK:
	case sandbox.Exited:
		os.Exit(int(r.ExitCode))
	case sandbox.Timeout:
		os.Exit(124)
	case sandbox.MemoryLimit, sandbox.OutputLimit:
		os.Exit(125)
	default:
		os.Exit(126)
	}
}
