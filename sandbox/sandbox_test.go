package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
)

// The guests are real Go programs, compiled to WebAssembly for these tests.
var (
	wasm  = map[string][]byte{}
	cache = wazero.NewCompilationCache()
	ctx   = context.Background()
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "guests")
	if err != nil {
		panic(err)
	}
	for _, g := range []struct{ name, mode string }{
		{"wordcount", ""}, {"snoop", ""}, {"hostile", ""}, {"kvuser", ""}, {"adder", "c-shared"},
	} {
		out := filepath.Join(dir, g.name+".wasm")
		args := []string{"build", "-o", out}
		if g.mode != "" {
			args = append(args, "-buildmode="+g.mode)
		}
		cmd := exec.Command("go", append(args, "./guests/"+g.name)...)
		cmd.Dir = ".."
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
		if b, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("building guest %s: %v\n%s", g.name, err, b)
			os.Exit(1)
		}
		if wasm[g.name], err = os.ReadFile(out); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func load(t testing.TB, name string, l Limits, g Grants) *Plugin {
	t.Helper()
	p, err := Load(ctx, name, wasm[name], l, g, cache)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close(ctx) })
	return p
}

func TestWellBehavedPlugin(t *testing.T) {
	p := load(t, "wordcount", Limits{}, Grants{})
	r := p.Run(ctx, []byte(`{"text":"the cat saw the dog.\nThe dog ran."}`))
	if r.Outcome != OK {
		t.Fatalf("%s: %v\n%s", r.Outcome, r.Err, r.Stderr)
	}
	var out struct {
		Words int `json:"words"`
		Lines int `json:"lines"`
		Top   []struct {
			Word  string `json:"word"`
			Count int    `json:"count"`
		} `json:"top"`
	}
	if err := json.Unmarshal(r.Stdout, &out); err != nil {
		t.Fatalf("output %q: %v", r.Stdout, err)
	}
	if out.Words != 8 || out.Lines != 2 || out.Top[0].Word != "the" || out.Top[0].Count != 3 {
		t.Fatalf("got %+v", out)
	}
	if r := p.Run(ctx, []byte(`not json`)); r.Outcome != Exited || r.ExitCode != 2 {
		t.Fatalf("bad input: %s %d", r.Outcome, r.ExitCode)
	}
}

type snoopReport struct {
	Env    []string          `json:"env"`
	Files  map[string]string `json:"files"`
	Root   any               `json:"root"`
	Write  string            `json:"write"`
	Net    string            `json:"net"`
	Time   string            `json:"time"`
	Random string            `json:"random"`
}

func snoop(t *testing.T, g Grants) (snoopReport, []byte) {
	t.Helper()
	r := load(t, "snoop", Limits{}, g).Run(ctx, nil)
	if r.Outcome != OK {
		t.Fatalf("%s: %v\n%s", r.Outcome, r.Err, r.Stderr)
	}
	var rep snoopReport
	if err := json.Unmarshal(r.Stdout, &rep); err != nil {
		t.Fatal(err)
	}
	return rep, r.Stdout
}

func TestNothingIsReachableByDefault(t *testing.T) {
	t.Setenv("HOST_SECRET", "sk-do-not-leak")
	rep, raw := snoop(t, Grants{})
	if len(rep.Env) != 0 {
		t.Errorf("the plugin saw environment variables: %v", rep.Env)
	}
	for path, got := range rep.Files {
		if !strings.HasPrefix(got, "error:") {
			t.Errorf("the plugin read %s", path)
		}
	}
	if s, ok := rep.Root.(string); !ok || !strings.HasPrefix(s, "error:") {
		t.Errorf("the plugin listed /: %v", rep.Root)
	}
	if !strings.HasPrefix(rep.Write, "error:") || !strings.HasPrefix(rep.Net, "error:") {
		t.Errorf("write=%q net=%q", rep.Write, rep.Net)
	}
	if !strings.HasPrefix(rep.Time, "2022-01-01") {
		t.Errorf("time %q: expected wazero's fake clock (starts at 2022-01-01)", rep.Time)
	}
	if strings.Contains(string(raw), "sk-do-not-leak") {
		t.Error("the secret leaked")
	}
	_, again := snoop(t, Grants{})
	if string(raw) != string(again) {
		t.Errorf("two runs differ; without RealClock a run should be fully deterministic:\n%s\n%s", raw, again)
	}
}

func TestGrantsOpenOnlyWhatTheySay(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("runbook v3"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, _ := snoop(t, Grants{DataDir: dir, RealClock: true})
	if rep.Files["/data/notes.txt"] != "runbook v3" {
		t.Errorf("/data/notes.txt = %q", rep.Files["/data/notes.txt"])
	}
	if !strings.HasPrefix(rep.Write, "error:") {
		t.Errorf("the data dir should be read-only, write = %q", rep.Write)
	}
	for _, p := range []string{"/etc/passwd", "C:/Windows/win.ini", "../../go.mod"} {
		if !strings.HasPrefix(rep.Files[p], "error:") {
			t.Errorf("granting /data also exposed %s", p)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "written.txt")); err == nil {
		t.Error("the plugin wrote into the host directory")
	}
	ts, err := time.Parse(time.RFC3339Nano, rep.Time)
	if err != nil || time.Since(ts).Abs() > time.Minute {
		t.Errorf("RealClock: plugin time %q is not now", rep.Time)
	}
	other, _ := snoop(t, Grants{RealClock: true})
	if other.Random == rep.Random {
		t.Error("RealClock should also give real randomness")
	}
}

func TestLimits(t *testing.T) {
	cases := []struct {
		mode   string
		limits Limits
		want   Outcome
		code   uint32
		within time.Duration
		stderr string
	}{
		{"loop", Limits{Timeout: 300 * time.Millisecond}, Timeout, 0, 3 * time.Second, ""},
		{"alloc", Limits{MemoryMiB: 32, Timeout: 10 * time.Second}, MemoryLimit, 2, 10 * time.Second, "out of memory"},
		{"spew", Limits{MaxOutput: 4096, Timeout: 10 * time.Second}, OutputLimit, 0, 5 * time.Second, ""},
		{"panic", Limits{}, Exited, 2, 3 * time.Second, "plugin bug"},
		{"exit3", Limits{}, Exited, 3, 3 * time.Second, "giving up"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			p := load(t, "hostile", tc.limits, Grants{})
			r := p.Run(ctx, []byte(`{"mode":"`+tc.mode+`"}`))
			if r.Outcome != tc.want {
				t.Fatalf("outcome %s (exit %d, err %v), want %s\nstderr: %.300s", r.Outcome, r.ExitCode, r.Err, tc.want, r.Stderr)
			}
			if tc.code != 0 && r.ExitCode != tc.code {
				t.Fatalf("exit code %d, want %d", r.ExitCode, tc.code)
			}
			if r.Duration > tc.within {
				t.Fatalf("took %s, want under %s", r.Duration, tc.within)
			}
			if tc.stderr != "" && !strings.Contains(string(r.Stderr), tc.stderr) {
				t.Fatalf("stderr %.300q lacks %q", r.Stderr, tc.stderr)
			}
			if tc.want == OutputLimit && len(r.Stdout)+len(r.Stderr) != 4096 {
				t.Fatalf("kept %d bytes, want exactly 4096", len(r.Stdout)+len(r.Stderr))
			}
			t.Logf("%s: %s after %s", tc.mode, r.Outcome, r.Duration.Round(time.Millisecond))
			// The same plugin, right after, still works: the host is fine.
			if ok := p.Run(ctx, []byte(`{"mode":"none"}`)); ok.Outcome != OK || !strings.Contains(string(ok.Stdout), `"ok":true`) {
				t.Fatalf("after %s the plugin no longer runs: %s %v", tc.mode, ok.Outcome, ok.Err)
			}
		})
	}
}

// unknownImport is a hand-written WebAssembly module that imports env.system.
var unknownImport = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // "\0asm", version 1
	0x01, 0x04, 0x01, 0x60, 0x00, 0x00, // type section: one func () -> ()
	0x02, 0x0e, 0x01, // import section, 14 bytes, 1 import
	0x03, 0x65, 0x6e, 0x76, // module "env"
	0x06, 0x73, 0x79, 0x73, 0x74, 0x65, 0x6d, // name "system"
	0x00, 0x00, // a function of type 0
}

func TestCapabilities(t *testing.T) {
	if _, err := Load(ctx, "kvuser", wasm["kvuser"], Limits{}, Grants{}, cache); err == nil ||
		!strings.Contains(err.Error(), `needs the "kv" capability`) {
		t.Fatalf("loading without the grant: err = %v", err)
	}

	p := load(t, "kvuser", Limits{}, Grants{KV: map[string]string{"region": "ap-south-1", "tier": "gold"}})
	if got := p.Needs(); len(got) != 1 || got[0] != "kv" {
		t.Fatalf("needs = %v", got)
	}
	r := p.Run(ctx, []byte(`{"keys":["region","tier","password"]}`))
	if r.Outcome != OK {
		t.Fatalf("%s: %v\n%s", r.Outcome, r.Err, r.Stderr)
	}
	var out map[string]*string
	if err := json.Unmarshal(r.Stdout, &out); err != nil {
		t.Fatal(err)
	}
	if out["region"] == nil || *out["region"] != "ap-south-1" || *out["tier"] != "gold" || out["password"] != nil {
		t.Fatalf("got %s", r.Stdout)
	}
	if p.KVCalls() != 3 {
		t.Fatalf("kv calls = %d, want 3", p.KVCalls())
	}

	if _, err := Load(ctx, "sneaky", unknownImport, Limits{}, Grants{KV: map[string]string{}}, cache); err == nil ||
		!strings.Contains(err.Error(), "imports env.system") {
		t.Fatalf("unknown import: err = %v", err)
	}
	if _, err := Load(ctx, "junk", []byte("MZ not wasm"), Limits{}, Grants{}, cache); err == nil ||
		!strings.Contains(err.Error(), "not a valid WebAssembly module") {
		t.Fatalf("junk: err = %v", err)
	}
}

func TestReactorStateIsPerInstance(t *testing.T) {
	p := load(t, "adder", Limits{}, Grants{})
	a, err := p.Instance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	for _, c := range [][3]uint64{{2, 3, 5}, {40, 2, 42}} {
		got, err := a.Call(ctx, "add", c[0], c[1])
		if err != nil || got[0] != c[2] {
			t.Fatalf("add(%d,%d) = %v, %v", c[0], c[1], got, err)
		}
	}
	if n, _ := a.Call(ctx, "calls"); n[0] != 2 {
		t.Fatalf("calls on instance a = %d, want 2", n[0])
	}
	b, err := p.Instance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(ctx)
	if n, _ := b.Call(ctx, "calls"); n[0] != 0 {
		t.Fatalf("calls on a fresh instance = %d, want 0", n[0])
	}
	if _, err := a.Call(ctx, "rm_rf"); err == nil {
		t.Fatal("calling a function the plugin doesn't export should fail")
	}
}

func TestManyRunsAtOnce(t *testing.T) {
	p := load(t, "wordcount", Limits{Timeout: 20 * time.Second}, Grants{})
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := range 32 {
		wg.Go(func() {
			text := strings.Repeat("word ", i+1)
			r := p.Run(ctx, []byte(fmt.Sprintf(`{"text":%q}`, text)))
			if r.Outcome != OK || !strings.Contains(string(r.Stdout), fmt.Sprintf(`"words":%d`, i+1)) {
				errs <- fmt.Errorf("run %d: %s %s", i, r.Outcome, r.Stdout)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
