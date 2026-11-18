//go:build wasip1

// adder is a "reactor": no main work, just exported functions the host calls
// (Go 1.24's //go:wasmexport, built with -buildmode=c-shared). An instance
// keeps its state between calls, which is what the tests check.
package main

var calls int32

//go:wasmexport add
func add(a, b int32) int32 {
	calls++
	return a + b
}

//go:wasmexport calls
func callsMade() int32 { return calls }

func main() {}
