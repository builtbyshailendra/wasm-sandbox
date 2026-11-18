package sandbox

import (
	"testing"

	"github.com/tetratelabs/wazero"
)

// Where the time goes. Compiling a Go-built module (4-5 MB of Wasm) to
// machine code is the big cost; a disk cache skips it after the first time.
// Each Run then pays for a fresh instance, including the Go runtime's own
// start-up inside the guest. A reactor instance that's already running
// answers a call in about the time of an ordinary function call.

func BenchmarkLoad(b *testing.B) {
	b.Run("compile, no cache", func(b *testing.B) {
		for b.Loop() {
			p, err := Load(ctx, "wordcount", wasm["wordcount"], Limits{}, Grants{}, nil)
			if err != nil {
				b.Fatal(err)
			}
			_ = p.Close(ctx)
		}
	})
	b.Run("from a warm disk cache", func(b *testing.B) {
		dir := b.TempDir()
		warm, _ := wazero.NewCompilationCacheWithDir(dir)
		p, err := Load(ctx, "wordcount", wasm["wordcount"], Limits{}, Grants{}, warm)
		if err != nil {
			b.Fatal(err)
		}
		_ = p.Close(ctx)
		for b.Loop() {
			c, _ := wazero.NewCompilationCacheWithDir(dir) // as a new process would
			p, err := Load(ctx, "wordcount", wasm["wordcount"], Limits{}, Grants{}, c)
			if err != nil {
				b.Fatal(err)
			}
			_ = p.Close(ctx)
			_ = c.Close(ctx)
		}
	})
}

func BenchmarkRun(b *testing.B) {
	p := load(b, "wordcount", Limits{}, Grants{})
	in := []byte(`{"text":"the quick brown fox jumps over the lazy dog"}`)
	for b.Loop() {
		if r := p.Run(ctx, in); r.Outcome != OK {
			b.Fatal(r.Outcome, r.Err)
		}
	}
}

func BenchmarkReactor(b *testing.B) {
	p := load(b, "adder", Limits{}, Grants{})
	b.Run("new instance", func(b *testing.B) {
		for b.Loop() {
			in, err := p.Instance(ctx)
			if err != nil {
				b.Fatal(err)
			}
			_ = in.Close(ctx)
		}
	})
	b.Run("call on a live instance", func(b *testing.B) {
		in, err := p.Instance(ctx)
		if err != nil {
			b.Fatal(err)
		}
		defer in.Close(ctx)
		for b.Loop() {
			if _, err := in.Call(ctx, "add", 2, 3); err != nil {
				b.Fatal(err)
			}
		}
	})
}
