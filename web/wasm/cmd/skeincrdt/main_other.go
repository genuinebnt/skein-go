//go:build !(js && wasm)

// Command skeincrdt only makes sense as WebAssembly; build it with
// web/wasm/build.sh. This stub keeps `go build ./...` and `go vet ./...`
// working on the host.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "skeincrdt: build with GOOS=js GOARCH=wasm (see web/wasm/build.sh)")
	os.Exit(2)
}
