# Learning notes: wasm-sandbox

## 1. WebAssembly in five minutes

WebAssembly (Wasm) is a compact instruction format for a simple virtual machine. A **module** has functions, one or more **linear memories** (a plain byte array the module can read and write), a list of **imports** (functions the host must provide) and **exports** (functions the host can call).

Why it's a good sandbox:
- A module can only read and write its own linear memory. Every access is bounds-checked; there are no pointers into the host.
- It can't do anything else on its own. Files, clocks, the network, even printing: all of it goes through imported functions, and the host decides which imports exist.
- So the list of imports *is* the list of powers. Read it before running the code (`checkImports`) and you know the worst it can do.

**WASI** (the WebAssembly System Interface) is a standard set of imports that looks like a small operating system: `fd_read`, `fd_write`, `path_open`, `clock_time_get`, `random_get`... Preview 1 (`wasi_snapshot_preview1`) is what Go's `GOOS=wasip1` targets. The host (wazero here) implements each call and decides what's behind it: which directories exist, what the clock says, where stdout goes.

## 2. Pieces used here

| Piece | Where | One-line idea |
|---|---|---|
| wazero `Runtime` | `Load` | owns compiled code and the memory limit (`WithMemoryLimitPages`, 64 KiB pages) |
| `CompileModule` / `InstantiateModule` | `Load`, `Run` | compile once (slow), instantiate per run (fast) |
| `WithCloseOnContextDone` | `Load` | lets a context deadline stop even a tight loop; the mutation check shows it's essential |
| `ModuleConfig` | `moduleConfig` | stdin/stdout/stderr, args, env, mounts, clocks, randomness, all opt-in |
| `WithReadOnlyDirMount` | `DataDir` grant | one host directory, read-only, at `/data` |
| fake clock and randomness | wazero defaults | deterministic runs: same input, same output |
| host module builder | `instantiateKV` | Go functions the guest can import; they get the guest's `api.Memory` |
| `ImportedFunctions()` | `checkImports` | the capability check before any code runs |
| `//go:wasmimport` | `guests/kvuser` | a Go guest declares a host function it needs |
| `//go:wasmexport`, `-buildmode=c-shared` | `guests/adder` | a Go guest exports functions: a "reactor" |
| `NewCompilationCacheWithDir` | CLI | compiled machine code kept on disk: 4 s becomes 0.19 s |

**Command vs reactor.** A command runs `_start` (Go's `main`) once and exits: one fresh instance per job, nothing remembered. A reactor runs `_initialize` and then waits for the host to call its exports, keeping state between calls. Commands are simpler to make safe; reactors are much cheaper per call.

## 3. Explain it without notes

1. Why can't a Wasm plugin read `/etc/passwd` here, even though Go's `os.ReadFile` compiled fine?
2. What exactly happens when the infinite-loop plugin hits its timeout? Why wasn't the context deadline enough by itself?
3. How does the memory limit work, and why did the Go guest report "out of memory" rather than being killed?
4. Why does the output limit cancel the run instead of just dropping extra bytes?
5. Walk through `kv_get`: what does the guest pass, how does the host read it, and what stops a bad pointer from reading host memory?
6. Why check imports at load time when the call would fail anyway without the host module?
7. Why are two runs byte-identical by default, and when would you grant the real clock?
8. Command vs reactor: what's the difference, and what are the costs from the benchmarks?
9. Where does the 4-second compile go, and how does the disk cache help? What's the catch? (Hint: 54 MB for three modules.)
10. Compare this to running the plugin in a container. When would you pick each?
11. Name two things this sandbox doesn't protect against.

## 4. Your turn

1. **A Rust or TinyGo guest:** build `wordcount` in another language and compare its size, compile time and run time with the Go one.
2. **A new capability:** `http_get` limited to an allowlist of hosts. Decide what the guest passes, what the host checks, and what the audit log records.
3. **Fuel instead of time:** wazero has no metering, but you can count calls in a host function or use a function listener (`experimental.FunctionListenerFactory`). Try it and measure the slowdown.
4. **Connect it to mcp-gate:** expose a sandboxed plugin as an MCP tool, so an agent's call goes through the gate's policy and then runs in the sandbox.
