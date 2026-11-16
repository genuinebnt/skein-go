module github.com/genuinebnt/skein-go/tools/seed

go 1.24

require (
	github.com/genuinebnt/skein-go/libs/crdt v0.0.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
)

require golang.org/x/text v0.14.0 // indirect

// The CRDT engine lives in the same repository. The replace keeps the tool
// buildable with GOWORK=off; inside the workspace go.work resolves it anyway.
replace github.com/genuinebnt/skein-go/libs/crdt => ../../libs/crdt
