package trigram

import (
	"regexp"
	"regexp/syntax"
	"testing"
)

func queryFor(t testing.TB, pattern string) Query {
	t.Helper()
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		t.Fatal(err)
	}
	q := RegexpQuery(re)
	if err := q.CheckInvariants(); err != nil {
		t.Fatalf("%s: %v", pattern, err)
	}
	return q
}

func TestRegexpQueryOfCommonShapes(t *testing.T) {
	cases := []struct{ pattern, want string }{
		{"abc", `"ABC"`},
		{"abcd", `"ABC" "BCD"`},
		{"(?i)abc", `"ABC"`},
		{"ab", "+"},
		{"abc|xyz", `("ABC"|"XYZ")`},
		{"a.*bcd", `"BCD"`},
		{"[ab]cd", `("ACD"|"BCD")`},
		{"x+yz", `"XYZ"`},
		{"abc?", "+"},
		{"(abc)*", "+"},
		// Four digits are 10,000 strings, far past maxExact: only the trigrams
		// reaching one digit past the dash survive.
		{`\bINC-\d{4}\b`, `"INC" "NC-" ("C-0"|"C-1"|"C-2"|"C-3"|"C-4"|"C-5"|"C-6"|"C-7"|"C-8"|"C-9")`},
		{"[^a]bc", "+"},
		{"", "+"},
		{"[^\\x00-\\x{10FFFF}]", "-"},
	}
	for _, c := range cases {
		if got := queryFor(t, c.pattern).String(); got != c.want {
			t.Errorf("%q: query %s, want %s", c.pattern, got, c.want)
		}
	}
}

func trigramSet(text string) func(Trigram) bool {
	set := map[Trigram]bool{}
	for _, t := range Trigrams(text) {
		set[t] = true
	}
	return func(t Trigram) bool { return set[t] }
}

func TestRegexpQueryNeverRulesOutAMatchingText(t *testing.T) {
	r := newRand(testSeed(t))
	for range 3000 {
		pattern := randomRegexp(r, 3)
		if r.IntN(2) == 0 {
			pattern = "(?i)" + pattern
		}
		re := regexp.MustCompile(pattern)
		q := queryFor(t, pattern)
		for range 20 {
			text := randomText(r, 25)
			if re.MatchString(text) && !q.Matches(trigramSet(text)) {
				t.Fatalf("pattern %q matches %q but query %s rules it out", pattern, text, q)
			}
		}
	}
}

func FuzzRegexpQuery(f *testing.F) {
	f.Add("abc", "xxabcxx")
	f.Add(`(?i)k\w+s`, "Kelvins")
	f.Add("a(b|cd)+e", "acdbe")
	f.Add("[a-c]{2,4}x", "abcx")
	f.Fuzz(func(t *testing.T, pattern, text string) {
		parsed, err := syntax.Parse(pattern, syntax.Perl)
		if err != nil {
			return
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return
		}
		q := RegexpQuery(parsed)
		if err := q.CheckInvariants(); err != nil {
			t.Fatalf("%q: %v", pattern, err)
		}
		if re.MatchString(text) && !q.Matches(trigramSet(text)) {
			t.Fatalf("pattern %q matches %q but query %s rules it out", pattern, text, q)
		}
	})
}
