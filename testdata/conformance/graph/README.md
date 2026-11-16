# Graph algorithm conformance fixtures

Language-neutral test cases for `libs/graph`. The Go implementation
(`libs/graph/conformance_test.go`) and the Rust port must both pass every
file here. The format and every rule the expected results follow are in
`docs/spec/graph.md`.

Each expected result was derived by hand from the spec before it was run,
and the reasoning is written in the fixture's and each query's `why` field.
A fixture whose `why` no longer matches the rules is a spec change, not a
test fix. Fixtures 012 and 013 exist because deliberately breaking a tie
rule (Louvain labels, Dijkstra hop counts) passed every earlier fixture.
