package domain

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/platform/ratelimit"
)

func TestOnlyTheFiveSessionRoutesArePublic(t *testing.T) {
	for _, m := range []string{MethodSignUp, MethodSignIn, MethodSignInWithOAuth, MethodRefresh, MethodSignOut} {
		if !PolicyFor(m, http.MethodPost).Public {
			t.Errorf("%s should be public", m)
		}
	}
	for _, m := range []string{"/skein.v1.IdentityService/GetUser", "/skein.v1.IdentityService/Authorize", MethodApplyOps} {
		if PolicyFor(m, http.MethodPost).Public {
			t.Errorf("%s should need a token", m)
		}
	}
}

func TestSearchAndSyncRoutesSpendTheirOwnBudgets(t *testing.T) {
	cases := map[string]Budget{
		MethodSearch:                         BudgetSearch,
		MethodSuggestTitles:                  BudgetSearch,
		MethodSemanticSearch:                 BudgetSearch,
		MethodApplyOps:                       BudgetSync,
		"/skein.v1.DocumentService/GetNote":  BudgetAPI,
		"/skein.v1.AgentService/CreateChat":  BudgetAPI,
		"/skein.v1.GraphService/BlastRadius": BudgetAPI,
	}
	for m, want := range cases {
		if got := PolicyFor(m, http.MethodGet).Budget; got != want {
			t.Errorf("%s: budget %s, want %s", m, got, want)
		}
	}
}

func TestIdempotencyKeysApplyToAuthenticatedNonIdempotentPostsOnly(t *testing.T) {
	cases := []struct {
		method, verb string
		want         bool
	}{
		{"/skein.v1.DocumentService/CreateNote", http.MethodPost, true},
		{"/skein.v1.DocumentService/Undo", http.MethodPost, true},
		{"/skein.v1.DocumentService/GetNote", http.MethodGet, false},
		{"/skein.v1.IdentityService/SetNoteRole", http.MethodPut, false},
		{MethodApplyOps, http.MethodPost, false},
		{MethodMarkRead, http.MethodPost, false},
		{MethodSignIn, http.MethodPost, false}, // responses carry tokens
		{MethodSignUp, http.MethodPost, false},
	}
	for _, c := range cases {
		if got := PolicyFor(c.method, c.verb).AcceptsIdempotencyKey; got != c.want {
			t.Errorf("%s %s: accepts key = %v, want %v", c.verb, c.method, got, c.want)
		}
	}
}

func TestDefaultLimitsCoverEveryBudgetAndSubject(t *testing.T) {
	limits := DefaultLimits()
	if err := limits.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, b := range Budgets {
		user, ip := limits[BudgetKey{b, SubjectUser}], limits[BudgetKey{b, SubjectIP}]
		if ip.Rate < user.Rate || ip.Burst < user.Burst {
			t.Errorf("%s: per-IP limit %+v is tighter than per-user %+v", b, ip, user)
		}
	}
	delete(limits, BudgetKey{BudgetAgent, SubjectIP})
	if err := limits.Validate(); !errors.Is(err, ErrInvalidLimit) {
		t.Errorf("missing limit: got %v, want ErrInvalidLimit", err)
	}
}

func TestParseLimit(t *testing.T) {
	got, err := ParseLimit("600/1m:100")
	if err != nil || got != (ratelimit.Limit{Rate: 600, Period: time.Minute, Burst: 100}) {
		t.Fatalf("ParseLimit = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "600", "600/1m", "x/1m:1", "1/x:1", "1/1m:x", "0/1m:1", "1/1m:0", "2000000/1ms:1"} {
		if _, err := ParseLimit(bad); !errors.Is(err, ErrInvalidLimit) {
			t.Errorf("ParseLimit(%q) = %v, want ErrInvalidLimit", bad, err)
		}
	}
}

func TestIPKeyGroupsIPv6ByPrefixAndUnmapsIPv4(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":                   "203.0.113.7",
		"::ffff:203.0.113.7":            "203.0.113.7",
		"2001:db8:1:2:3:4:5:6":          "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff:ffff:1": "2001:db8:1:2::/64",
	}
	for in, want := range cases {
		if got := IPKey(netip.MustParseAddr(in)); got != want {
			t.Errorf("IPKey(%s) = %s, want %s", in, got, want)
		}
	}
	if !strings.HasPrefix(LimiterKey(BudgetSearch, SubjectUser, "u1"), "search:user:") {
		t.Error("limiter keys must carry budget and subject")
	}
}
