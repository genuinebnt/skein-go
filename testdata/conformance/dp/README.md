# Dynamic programming conformance fixtures

Language-neutral test cases for `libs/dp`. The Go implementation
(`libs/dp/conformance_test.go`) and the Rust port must both pass every file
here with identical results. The format is defined in
`docs/spec/dp.md#9-conformance-fixtures`.

Most of these problems have several equally good answers, so most fixtures
pin a tie-breaking rule from the spec (which LCS, which diff matching, which
plan among equals, which chunking, which snippet, which segmentation). Each
expected result was derived by hand from the rules in the spec before it was
run, and the reasoning is in the fixture's `why` field. A fixture whose
`why` no longer matches the rules is a spec change, not a test fix.
