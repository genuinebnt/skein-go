package domain

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/genuinebnt/skein-go/services/gateway/internal/testseed"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/rendezvous.json from the current implementation")

// goldenFile pins RendezvousScore and Owner so that every gateway replica,
// including the Rust port, routes a note to the same instance.
type goldenFile struct {
	Why    string        `json:"why"`
	Scores []goldenScore `json:"scores"`
	Owners []goldenOwner `json:"owners"`
}

type goldenScore struct {
	Key    string `json:"key"`
	Member string `json:"member"`
	Score  string `json:"score"` // decimal uint64; JSON numbers lose precision past 2^53
}

type goldenOwner struct {
	Members []string `json:"members"`
	Key     string   `json:"key"`
	Owner   string   `json:"owner"`
}

func buildGolden() goldenFile {
	members := []string{"10.0.0.1:9090", "10.0.0.2:9090", "10.0.0.3:9090", "10.0.0.4:9090"}
	keys := []string{"", "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f", "01936f41-0000-7000-8000-000000000000", "note-ü"}
	g := goldenFile{Why: "Every gateway replica, in Go or Rust, must pick the same documents instance " +
		"for a note, or two instances would each run an actor for it. Score is " +
		"splitmix64(fnv1a64(key || 0x00 || member)); owner is the highest score, ties to the smaller member."}
	for _, k := range keys {
		for _, m := range members[:2] {
			g.Scores = append(g.Scores, goldenScore{k, m, fmt.Sprint(RendezvousScore(k, m))})
		}
	}
	for n := 1; n <= len(members); n++ {
		ring := NewRing(members[:n])
		for _, k := range keys {
			owner, _ := ring.Owner(k)
			g.Owners = append(g.Owners, goldenOwner{members[:n], k, owner})
		}
	}
	return g
}

func TestRendezvousMatchesGoldenValues(t *testing.T) {
	path := filepath.Join("testdata", "rendezvous.json")
	got := buildGolden()
	if *updateGolden {
		data, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want goldenFile
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	for i, w := range want.Scores {
		if s := fmt.Sprint(RendezvousScore(w.Key, w.Member)); s != w.Score {
			t.Errorf("score %d (%q, %q) = %s, want %s", i, w.Key, w.Member, s, w.Score)
		}
	}
	for i, w := range want.Owners {
		if o, _ := NewRing(w.Members).Owner(w.Key); o != w.Owner {
			t.Errorf("owner %d of %q among %v = %s, want %s", i, w.Key, w.Members, o, w.Owner)
		}
	}
}

func TestRingNormalisesMembers(t *testing.T) {
	r := NewRing([]string{"b", "", "a", "b", "c"})
	if err := r.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	if !r.Equal(NewRing([]string{"c", "a", "b"})) || r.Len() != 3 {
		t.Errorf("members = %v", r.Members())
	}
	if _, ok := (Ring{}).Owner("x"); ok {
		t.Error("an empty ring has no owner")
	}
	if err := (Ring{members: []string{"b", "a"}}).CheckInvariants(); err == nil {
		t.Error("CheckInvariants accepted unsorted members")
	}
}

func randomMembers(rng interface{ IntN(int) int }, n int) []string {
	members := make([]string, n)
	for i := range members {
		members[i] = fmt.Sprintf("10.%d.%d.%d:9090", rng.IntN(256), rng.IntN(256), rng.IntN(256))
	}
	return members
}

// Removing a member moves only the notes it owned; adding one moves notes
// only to the newcomer. This is why rendezvous hashing was chosen: a pod
// restart reshuffles 1/n of the sync streams, not all of them.
func TestMembershipChangesMoveOnlyTheAffectedKeys(t *testing.T) {
	rng := testseed.Rand(testseed.Seed(t))
	for round := 0; round < 50; round++ {
		members := randomMembers(rng, 2+rng.IntN(8))
		before := NewRing(members)
		if err := before.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
		removed := before.Members()[rng.IntN(before.Len())]
		var rest []string
		for _, m := range before.Members() {
			if m != removed {
				rest = append(rest, m)
			}
		}
		after := NewRing(rest)
		added := NewRing(append(before.Members(), "192.168.0.1:9090"))
		for _, ring := range []Ring{after, added} {
			if err := ring.CheckInvariants(); err != nil {
				t.Fatal(err)
			}
		}
		for k := 0; k < 500; k++ {
			key := fmt.Sprintf("note-%d-%d", round, k)
			was, _ := before.Owner(key)
			if now, _ := after.Owner(key); was != removed && now != was {
				t.Fatalf("removing %s moved %s from %s to %s", removed, key, was, now)
			}
			if now, _ := added.Owner(key); now != was && now != "192.168.0.1:9090" {
				t.Fatalf("adding a member moved %s from %s to %s", key, was, now)
			}
		}
	}
}

// With n members each should own about 1/n of the notes; the bound is five
// standard deviations of a binomial share, so a correct hash fails it with
// negligible probability while a skewed one (FNV without the finaliser on
// similar addresses) fails it reliably.
func TestOwnershipIsBalanced(t *testing.T) {
	rng := testseed.Rand(testseed.Seed(t))
	const keys = 40000
	for _, n := range []int{2, 3, 8} {
		members := make([]string, n)
		for i := range members {
			members[i] = fmt.Sprintf("10.0.0.%d:9090", i+1) // deliberately similar
		}
		ring := NewRing(members)
		counts := map[string]int{}
		for k := 0; k < keys; k++ {
			owner, _ := ring.Owner(fmt.Sprintf("%016x", rng.Uint64()))
			counts[owner]++
		}
		p := 1 / float64(n)
		tolerance := 5 * math.Sqrt(keys*p*(1-p))
		for _, m := range members {
			if diff := math.Abs(float64(counts[m]) - keys*p); diff > tolerance {
				t.Errorf("n=%d: %s owns %d, want %.0f ± %.0f", n, m, counts[m], keys*p, tolerance)
			}
		}
	}
}

func TestOwnerIgnoresInputOrder(t *testing.T) {
	rng := testseed.Rand(testseed.Seed(t))
	members := randomMembers(rng, 6)
	reversed := make([]string, len(members))
	for i, m := range members {
		reversed[len(members)-1-i] = m
	}
	a, b := NewRing(members), NewRing(reversed)
	for k := 0; k < 200; k++ {
		key := fmt.Sprint(rng.Uint64())
		if x, _ := a.Owner(key); x != func() string { y, _ := b.Owner(key); return y }() {
			t.Fatalf("order changed the owner of %s", key)
		}
	}
}
