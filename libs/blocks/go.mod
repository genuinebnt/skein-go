module github.com/genuinebnt/skein-go/libs/blocks

go 1.24

require github.com/genuinebnt/skein-go/libs/crdt v0.0.0

// The CRDT engine lives in the same repository. The replace keeps the
// library buildable with GOWORK=off; inside the workspace go.work resolves it
// anyway.
replace github.com/genuinebnt/skein-go/libs/crdt => ../crdt
