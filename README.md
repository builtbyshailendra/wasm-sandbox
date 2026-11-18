# wasm-sandbox

Runs untrusted code (a third-party plugin, or a tool an AI agent wrote) inside WebAssembly, with hard limits. By default a plugin gets nothing but its input: no files, no environment variables, no network, no real clock, no real randomness, and it is stopped when it runs too long, uses too much memory, or prints too much.

Status: built step by step, locally. Not published.

## Build log

### Step 1: the sandbox and its tests

`sandbox/` runs WASI plugins with [wazero](https://wazero.io) v1.12, a WebAssembly runtime written in pure Go (no cgo). The plugins in `guests/` are ordinary Go programs compiled to WebAssembly with `GOOS=wasip1 GOARCH=wasm`; the tests compile them first.

| Guest | What it does |
|---|---|
| `wordcount` | a well-behaved plugin: JSON in, JSON out |
| `snoop` | tries to read env vars, `/etc/passwd`, `C:/Windows/win.ini`, `../../go.mod`, list `/`, write a file, open a TCP connection; reports what it saw |
| `hostile` | an infinite loop, endless allocation, endless printing, a panic, `exit(3)` |
| `kvuser` | calls a host function, `sandbox.kv_get`, through Go's `//go:wasmimport` |
| `adder` | a *reactor* (`-buildmode=c-shared`, `//go:wasmexport`): the host calls its functions directly |

What's enforced, and the evidence from the tests on this laptop:

| Rule | How | Test result |
|---|---|---|
| no environment variables | none passed in | a `HOST_SECRET` set on the host never appeared |
| no files | no directories mounted | every read, the directory listing and the write failed |
| no network | WASI preview 1 has no way to open a socket | `net.Dial` failed |
| deterministic by default | wazero's fake clock (starts 2022-01-01, +1 ms per read) and fixed random bytes | two runs gave byte-identical output |
| time limit | context deadline + `WithCloseOnContextDone` | an infinite loop with a 300 ms limit stopped at 302 ms |
| memory limit | `WithMemoryLimitPages` | allocating forever under 32 MiB failed in 72 ms ("out of memory") |
| output limit | a shared stdout+stderr cap that cancels the run | printing forever with a 4 KB cap stopped at exactly 4096 bytes after 14 ms |
| capabilities | the module's imports are checked **at load time** against its grants | `kvuser` refused without the `kv` grant; a hand-written module importing `env.system` refused |
| no state between callers | a fresh instance per run (and per reactor instance) | a second `adder` instance started with 0 calls |

After every hostile run, the same plugin ran normally again: a misbehaving plugin doesn't hurt the host. 32 runs at once from one compiled plugin all returned the right answers.

Grants open exactly one thing each: `DataDir` mounts one host directory read-only at `/data` (the plugin read it, its write failed, nothing else became readable); `RealClock` gives the real time and real randomness.

Checking the tests catch what they claim: with `WithCloseOnContextDone` switched off, the infinite-loop test never finished (the test binary was killed at 40 s). It stops the loop; the context deadline alone doesn't.

What broke: nothing in the tests on the first run.
