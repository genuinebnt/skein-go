// Command seed generates Skein's seed datasets: deterministic, realistic
// data plus the ground truth tests check against. See docs/seed-data.md.
//
//	go run ./tools/seed -profile small -seed 1234 -out testdata/seed/small
//
// The same profile and seed always produce byte-identical output. The
// output directory must be empty, missing, or a previous seed dataset (it
// has a manifest.json), which is replaced.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/genuinebnt/skein-go/tools/seed/internal/generate"
)

func main() {
	profile := flag.String("profile", "small", "dataset size: small, medium, large or stress")
	seed := flag.Uint64("seed", 1234, "root random seed")
	out := flag.String("out", "", "output directory (required)")
	workers := flag.Int("workers", 0, "parallel note workers (0 = one per CPU); does not change the output")
	quiet := flag.Bool("quiet", false, "do not print progress")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "seed: -out is required")
		flag.Usage()
		os.Exit(2)
	}
	prof, err := generate.LookupProfile(*profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(2)
	}
	var log io.Writer = os.Stderr
	if *quiet {
		log = nil
	}
	sum, err := generate.Run(generate.Options{Profile: prof, Seed: *seed, Out: *out, Workers: *workers, Log: log})
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	keys := make([]string, 0, len(sum.Counts))
	for k := range sum.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("profile %s, seed %d: %.1f MB in %s\n", prof.Name, *seed, float64(sum.Bytes)/1e6, sum.Duration.Round(1e7))
	for _, k := range keys {
		fmt.Printf("  %-44s %d\n", k, sum.Counts[k])
	}
}
