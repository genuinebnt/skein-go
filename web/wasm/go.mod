module github.com/genuinebnt/skein-go/web/wasm

go 1.24

require (
	github.com/genuinebnt/skein-go/libs/blocks v0.0.0
	github.com/genuinebnt/skein-go/libs/crdt v0.0.0
)

// Both libraries live in this repository. The replaces keep the module
// buildable with GOWORK=off (TinyGo builds that way); inside the workspace
// go.work resolves them anyway.
replace (
	github.com/genuinebnt/skein-go/libs/blocks => ../../libs/blocks
	github.com/genuinebnt/skein-go/libs/crdt => ../../libs/crdt
)
