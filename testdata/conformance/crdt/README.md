# Sequence CRDT conformance fixtures

Language-neutral test cases for the sequence CRDT. The Go implementation
(`libs/crdt/conformance_test.go`) and the Rust port must both pass every file
here with identical results. The format is defined in
`docs/spec/crdt-sequence.md#conformance-fixtures`.

Each expected result was derived by hand from the rules in the spec before it
was run, and the reasoning is written in the fixture's `why` field. A fixture
whose `why` no longer matches the rules is a spec change, not a test fix.
