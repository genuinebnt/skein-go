module github.com/genuinebnt/skein-go/libs/trigram

go 1.24

require github.com/genuinebnt/skein-go/libs/dp v0.0.0

// The dp library lives in the same repository. The replace keeps the module
// buildable with GOWORK=off; inside the workspace go.work resolves it anyway.
replace github.com/genuinebnt/skein-go/libs/dp => ../dp
