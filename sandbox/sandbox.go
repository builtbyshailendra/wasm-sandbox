// Package sandbox runs untrusted WebAssembly plugins with hard limits.
//
// A plugin is a WASI program: JSON in on stdin, JSON out on stdout. By
// default it gets nothing else: no files, no environment variables, no
// network, no real clock, no real randomness. Each Run is a fresh instance,
// so nothing carries over from one caller to the next. Anything more is a
// grant: a read-only data directory, the real clock, or a host capability
// such as a key-value store, and a plugin that imports a capability it wasn't
// granted is refused before any of its code runs.
package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// Limits bound one run.
type Limits struct {
	Timeout   time.Duration // wall-clock time; default 2s
	MemoryMiB uint32        // linear memory; default 64 MiB
	MaxOutput int           // stdout + stderr bytes; default 1 MiB
}

func (l Limits) withDefaults() Limits {
	if l.Timeout <= 0 {
		l.Timeout = 2 * time.Second
	}
	if l.MemoryMiB == 0 {
		l.MemoryMiB = 64
	}
	if l.MaxOutput <= 0 {
		l.MaxOutput = 1 << 20
	}
	return l
}

// Grants are what a plugin may use beyond pure computation.
type Grants struct {
	KV        map[string]string // the "kv" capability: sandbox.kv_get over these entries
	DataDir   string            // a host directory, mounted read-only at /data
	RealClock bool              // the real wall clock, monotonic clock, sleep and randomness
}

// Outcome says how a run ended.
type Outcome string

const (
	OK          Outcome = "ok"           // exited 0
	Exited      Outcome = "exited"       // exited with a non-zero code
	Timeout     Outcome = "timeout"      // killed at Limits.Timeout
	MemoryLimit Outcome = "memory_limit" // ran out of memory under Limits.MemoryMiB
	OutputLimit Outcome = "output_limit" // killed for writing more than Limits.MaxOutput
	Trap        Outcome = "trap"         // a WebAssembly trap (e.g. out-of-bounds access)
)

// Result is one run.
type Result struct {
	Outcome  Outcome
	ExitCode uint32
	Stdout   []byte
	Stderr   []byte
	Duration time.Duration
	Err      error // the underlying error, if any
}

// Plugin is a compiled module plus the limits and grants it runs with.
// Safe for concurrent use: every Run is its own instance.
type Plugin struct {
	name     string
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	limits   Limits
	grants   Grants
	needs    []string
	kvCalls  *kvCounter
}

// Load compiles a plugin and checks its imports against its grants.
// cache may be nil; sharing one across plugins and Hosts skips recompiling
// the same bytes.
func Load(ctx context.Context, name string, wasm []byte, limits Limits, grants Grants, cache wazero.CompilationCache) (*Plugin, error) {
	limits = limits.withDefaults()
	cfg := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(limits.MemoryMiB * 16). // a page is 64 KiB
		WithCloseOnContextDone(true)                 // a timeout stops even a tight loop
	if cache != nil {
		cfg = cfg.WithCompilationCache(cache)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	ok := false
	defer func() {
		if !ok {
			_ = rt.Close(ctx)
		}
	}()
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return nil, err
	}
	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, fmt.Errorf("%s: not a valid WebAssembly module: %w", name, err)
	}
	needs, err := checkImports(name, compiled, grants)
	if err != nil {
		return nil, err
	}
	p := &Plugin{name: name, rt: rt, compiled: compiled, limits: limits, grants: grants, needs: needs, kvCalls: &kvCounter{}}
	if grants.KV != nil {
		if err := instantiateKV(ctx, rt, grants.KV, p.kvCalls); err != nil {
			return nil, err
		}
	}
	ok = true
	return p, nil
}

// Needs lists the capabilities the plugin imports.
func (p *Plugin) Needs() []string { return append([]string(nil), p.needs...) }

// KVCalls reports how many times the plugin has called kv_get.
func (p *Plugin) KVCalls() int64 { return p.kvCalls.n.Load() }

// Close frees the plugin's runtime.
func (p *Plugin) Close(ctx context.Context) error { return p.rt.Close(ctx) }

// Run executes the plugin once with input on stdin.
func (p *Plugin) Run(ctx context.Context, input []byte) Result {
	ctx, cancel := context.WithTimeout(ctx, p.limits.Timeout)
	defer cancel()
	out := &capped{limit: p.limits.MaxOutput, stop: cancel}
	cfg := p.moduleConfig(bytes.NewReader(input), out.stream(&out.stdout), out.stream(&out.stderr)).
		WithName("") // anonymous, so many instances can run at once

	start := time.Now()
	mod, err := p.rt.InstantiateModule(ctx, p.compiled, cfg)
	res := Result{Duration: time.Since(start)}
	if mod != nil {
		_ = mod.Close(context.Background())
	}
	res.Stdout, res.Stderr = out.bytes()
	res.Outcome, res.ExitCode, res.Err = classify(err, out.exceeded(), res.Stderr)
	return res
}

func (p *Plugin) moduleConfig(stdin *bytes.Reader, stdout, stderr *capStream) wazero.ModuleConfig {
	cfg := wazero.NewModuleConfig().
		WithArgs(p.name). // argv[0] only; no environment variables at all
		WithStdin(stdin).WithStdout(stdout).WithStderr(stderr)
	if p.grants.DataDir != "" {
		cfg = cfg.WithFSConfig(wazero.NewFSConfig().WithReadOnlyDirMount(p.grants.DataDir, "/data"))
	}
	if p.grants.RealClock {
		cfg = cfg.WithSysWalltime().WithSysNanotime().WithSysNanosleep().WithRandSource(rand.Reader)
	}
	// Without RealClock, wazero's defaults apply: a fake clock that moves
	// 1ms per reading, sleeps that return at once, and fixed "random"
	// bytes. The same input then gives the same output, every time.
	return cfg
}

func classify(err error, outputExceeded bool, stderr []byte) (Outcome, uint32, error) {
	if err == nil {
		return OK, 0, nil
	}
	var exit *sys.ExitError
	if errors.As(err, &exit) {
		switch code := exit.ExitCode(); {
		case code == 0:
			return OK, 0, nil
		case outputExceeded:
			return OutputLimit, code, err
		case code == sys.ExitCodeDeadlineExceeded:
			return Timeout, code, err
		case code == sys.ExitCodeContextCanceled:
			return Timeout, code, err
		case bytes.Contains(stderr, []byte("out of memory")):
			// Go's runtime reports a failed memory.grow as a fatal error.
			return MemoryLimit, code, err
		default:
			return Exited, code, err
		}
	}
	if outputExceeded {
		return OutputLimit, 0, err
	}
	if strings.Contains(err.Error(), "out of memory") {
		return MemoryLimit, 0, err
	}
	return Trap, 0, err
}

// capped collects stdout and stderr up to a shared byte limit. Going over it
// cancels the run's context, which stops the module: a plugin that ignores
// write errors and keeps printing can't spin until the timeout.
type capped struct {
	mu             sync.Mutex
	limit          int
	used           int
	over           bool
	stdout, stderr bytes.Buffer
	stop           context.CancelFunc
}

type capStream struct {
	c   *capped
	buf *bytes.Buffer
}

func (c *capped) stream(buf *bytes.Buffer) *capStream { return &capStream{c: c, buf: buf} }

func (s *capStream) Write(p []byte) (int, error) {
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.over {
		return 0, errOutputLimit
	}
	room := c.limit - c.used
	if len(p) > room {
		s.buf.Write(p[:room])
		c.used, c.over = c.limit, true
		c.stop()
		return room, errOutputLimit
	}
	s.buf.Write(p)
	c.used += len(p)
	return len(p), nil
}

var errOutputLimit = errors.New("output limit reached")

func (c *capped) exceeded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.over
}

func (c *capped) bytes() ([]byte, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.stdout.Bytes()), bytes.Clone(c.stderr.Bytes())
}
