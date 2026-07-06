// Helper to access runtime.GOMAXPROCS without a circular import in otel.go.
package main

import (
	"runtime"
	"sync"
)

var import_runtime_once sync.Once

func runtimeGOMAXPROCS(n int) int {
	return runtime.GOMAXPROCS(n)
}
