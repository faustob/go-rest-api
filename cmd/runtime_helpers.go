package main

import "runtime"

// runtimeMaxProcs returns runtime.GOMAXPROCS(0) — the current goroutine
// scheduler parallelism limit, used as a proxy for the HTTP worker pool size.
func runtimeMaxProcs() int {
	return runtime.GOMAXPROCS(0)
}
