package hostlist

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/0xbenc/ssherpa/internal/sshconfig"
)

// referenceApplyParsedEffectiveValues is a verbatim copy of the
// pre-optimization applyParsedEffectiveValues: for every alias it rescans
// all blocks in graph order. It is the differential oracle for
// TestBuildMatchesReference and must stay byte-for-byte faithful to the
// original algorithm (first-value-wins, de-duped IdentityFiles append
// order, equality-first pattern matching, negation short-circuit).
func referenceApplyParsedEffectiveValues(alias *Alias, graph *sshconfig.Graph) {
	seenIdentity := map[string]bool{}

	for _, block := range graph.Blocks {
		if !blockMatchesName(block, alias.Name) {
			continue
		}

		for _, option := range block.Options {
			value := firstValue(option.Values)
			if value == "" {
				continue
			}

			switch option.Keyword {
			case "hostname":
				if alias.HostName == "" {
					alias.HostName = value
				}
			case "user":
				if alias.User == "" {
					alias.User = value
				}
			case "port":
				if alias.Port == "" {
					alias.Port = value
				}
			case "identityfile":
				if !seenIdentity[value] {
					alias.IdentityFiles = append(alias.IdentityFiles, value)
					seenIdentity[value] = true
				}
			}
		}
	}
}

func randomGraph(rng *rand.Rand) *sshconfig.Graph {
	literalPool := []string{
		"foo", "bar", "baz",
		"web1", "web2", "web3",
		"db1", "db2", "db3",
		"srv-a", "srv-b",
		"xy", `x\y`,
		"a[b",
	}
	globPool := []string{
		"web*", "w??", "h?st-*",
		"db[1-3]", "s[ab]rv-*",
		`x\\y`, `a\[b`,
		"a[",
	}
	negatedPool := []string{
		"!foo", "!web1",
		"!db[1-3]", "!s[ab]rv-a",
		`!x\\y`,
	}
	hostValues := []string{"h0.example.com", "h1.example.com", "h2.example.com", ""}
	userValues := []string{"alice", "bob", "git", ""}
	portValues := []string{"22", "2222", ""}
	keyValues := []string{"~/.ssh/k1", "~/.ssh/k2", "~/.ssh/k3", "~/.ssh/k1"}

	randomPattern := func(pool []string) string {
		return pool[rng.Intn(len(pool))]
	}
	randomValues := func(pool []string) []string {
		count := 1 + rng.Intn(3)
		values := make([]string, 0, count)
		for i := 0; i < count; i++ {
			values = append(values, pool[rng.Intn(len(pool))])
		}
		return values
	}

	numBlocks := 1 + rng.Intn(12)
	blocks := make([]sshconfig.HostBlock, 0, numBlocks)
	for b := 0; b < numBlocks; b++ {
		var patterns []string
		switch rng.Intn(4) {
		case 0:
			// Literal block, sometimes multi-pattern with duplicates.
			for i := 0; i < 1+rng.Intn(3); i++ {
				patterns = append(patterns, randomPattern(literalPool))
			}
		case 1:
			// Glob block, sometimes mixed with a literal.
			patterns = append(patterns, randomPattern(globPool))
			if rng.Intn(3) == 0 {
				patterns = append(patterns, randomPattern(literalPool))
			}
		case 2:
			// Negation, sometimes sharing the block with a literal the
			// negation excludes (e.g. "Host foo !foo").
			patterns = append(patterns, randomPattern(negatedPool))
			if rng.Intn(2) == 0 {
				patterns = append(patterns, randomPattern(literalPool))
			}
		default:
			// Mixed block, sometimes with empty or padded patterns.
			patterns = append(patterns,
				randomPattern(literalPool),
				randomPattern(globPool),
			)
			if rng.Intn(2) == 0 {
				patterns = append(patterns, randomPattern(negatedPool))
			}
			if rng.Intn(4) == 0 {
				patterns = append(patterns, "")
			}
			if rng.Intn(4) == 0 {
				patterns = append(patterns, " "+randomPattern(literalPool))
			}
		}
		if rng.Intn(8) == 0 {
			patterns = append(patterns, "")
		}
		rng.Shuffle(len(patterns), func(i, j int) {
			patterns[i], patterns[j] = patterns[j], patterns[i]
		})

		numOptions := rng.Intn(6)
		options := make([]sshconfig.Option, 0, numOptions)
		for o := 0; o < numOptions; o++ {
			switch rng.Intn(6) {
			case 0:
				options = append(options, sshconfig.Option{Keyword: "hostname", Values: randomValues(hostValues)})
			case 1:
				options = append(options, sshconfig.Option{Keyword: "user", Values: randomValues(userValues)})
			case 2:
				options = append(options, sshconfig.Option{Keyword: "port", Values: randomValues(portValues)})
			case 3:
				options = append(options, sshconfig.Option{Keyword: "identityfile", Values: randomValues(keyValues)})
			case 4:
				options = append(options, sshconfig.Option{Keyword: "proxyjump", Values: []string{"jumper"}})
			default:
				options = append(options, sshconfig.Option{Keyword: "hostname", Values: []string{}})
			}
		}
		rng.Shuffle(len(options), func(i, j int) {
			options[i], options[j] = options[j], options[i]
		})

		blocks = append(blocks, sshconfig.HostBlock{
			SourcePath:  fmt.Sprintf("random-%d", b),
			SourceLine:  b + 1,
			Patterns:    patterns,
			Conditional: rng.Intn(4) == 0,
			Options:     options,
		})
	}

	return &sshconfig.Graph{
		RootPath: "random",
		Blocks:   blocks,
	}
}

// TestBuildMatchesReference is the differential gate for the block-index
// optimization. Over 200 seeded random graphs it asserts that Build's
// effective values for every alias — hostname, user, port, and
// IdentityFiles including element order — equal the pre-index reference
// loop.
func TestBuildMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5eed))

	for trial := 0; trial < 200; trial++ {
		graph := randomGraph(rng)
		inventory := Build(graph, Options{All: true})

		for _, alias := range inventory.Aliases {
			want := Alias{Name: alias.Name}
			referenceApplyParsedEffectiveValues(&want, graph)

			if alias.HostName != want.HostName {
				t.Fatalf("trial %d alias %q: HostName = %q, reference %q\nblocks: %#v", trial, alias.Name, alias.HostName, want.HostName, graph.Blocks)
			}
			if alias.User != want.User {
				t.Fatalf("trial %d alias %q: User = %q, reference %q\nblocks: %#v", trial, alias.Name, alias.User, want.User, graph.Blocks)
			}
			if alias.Port != want.Port {
				t.Fatalf("trial %d alias %q: Port = %q, reference %q\nblocks: %#v", trial, alias.Name, alias.Port, want.Port, graph.Blocks)
			}
			if !sameStrings(alias.IdentityFiles, want.IdentityFiles) {
				t.Fatalf("trial %d alias %q: IdentityFiles = %#v, reference %#v\nblocks: %#v", trial, alias.Name, alias.IdentityFiles, want.IdentityFiles, graph.Blocks)
			}
		}
	}
}

// TestBuildTrapPatterns pins the classifier trap from the brief:
// filepath.Match honors [classes] and \escapes even though IsPattern only
// looks at *? and !. Such patterns must be treated as scan blocks, and
// invalid-glob patterns must still match their own literal spelling via
// the equality-first path.
func TestBuildTrapPatterns(t *testing.T) {
	graph := &sshconfig.Graph{
		RootPath: "trap",
		Blocks: []sshconfig.HostBlock{
			{SourcePath: "trap", SourceLine: 1, Patterns: []string{"db1"}, Options: []sshconfig.Option{{Keyword: "hostname", Values: []string{"db1.example.com"}}}},
			{SourcePath: "trap", SourceLine: 2, Patterns: []string{"db[1-3]"}, Options: []sshconfig.Option{{Keyword: "user", Values: []string{"dba"}}}},
			{SourcePath: "trap", SourceLine: 3, Patterns: []string{"xy"}, Options: []sshconfig.Option{{Keyword: "port", Values: []string{"22"}}}},
			{SourcePath: "trap", SourceLine: 4, Patterns: []string{`x\y`}, Options: []sshconfig.Option{{Keyword: "user", Values: []string{"esc"}}}},
			{SourcePath: "trap", SourceLine: 5, Patterns: []string{"a["}, Options: []sshconfig.Option{{Keyword: "port", Values: []string{"2222"}}}},
			{SourcePath: "trap", SourceLine: 6, Patterns: []string{"a[b"}, Options: []sshconfig.Option{{Keyword: "hostname", Values: []string{"ab.example.com"}}}},
			{SourcePath: "trap", SourceLine: 7, Patterns: []string{`a\[b`}, Options: []sshconfig.Option{{Keyword: "user", Values: []string{"brack"}}}},
			{SourcePath: "trap", SourceLine: 8, Patterns: []string{"foo", "!foo"}, Options: []sshconfig.Option{{Keyword: "hostname", Values: []string{"never.example.com"}}}},
			{SourcePath: "trap", SourceLine: 9, Patterns: []string{"foo"}, Options: []sshconfig.Option{{Keyword: "hostname", Values: []string{"foo.example.com"}}}},
			{SourcePath: "trap", SourceLine: 10, Patterns: []string{"dup"}, Options: []sshconfig.Option{{Keyword: "identityfile", Values: []string{"~/.ssh/kA"}}}},
			{SourcePath: "trap", SourceLine: 11, Patterns: []string{"dup"}, Options: []sshconfig.Option{{Keyword: "identityfile", Values: []string{"~/.ssh/kB"}}}},
			{SourcePath: "trap", SourceLine: 12, Patterns: []string{"dup", "dup"}, Options: []sshconfig.Option{{Keyword: "identityfile", Values: []string{"~/.ssh/kC"}}}},
		},
	}

	inventory := Build(graph, Options{All: true})

	cases := map[string]struct {
		hostname string
		user     string
		port     string
		keys     []string
	}{
		"db1":     {hostname: "db1.example.com", user: "dba"},
		"db[1-3]": {user: "dba"},
		"xy":      {port: "22", user: "esc"},
		`x\y`:     {user: "esc"},
		"a[":      {port: "2222"},
		"a[b":     {hostname: "ab.example.com", user: "brack"},
		`a\[b`:    {user: "brack"},
		"foo":     {hostname: "foo.example.com"},
		"dup":     {keys: []string{"~/.ssh/kA", "~/.ssh/kB", "~/.ssh/kC"}},
	}

	for name, want := range cases {
		alias := findAlias(t, inventory.Aliases, name)
		if alias.HostName != want.hostname {
			t.Fatalf("alias %q: HostName = %q, want %q", name, alias.HostName, want.hostname)
		}
		if alias.User != want.user {
			t.Fatalf("alias %q: User = %q, want %q", name, alias.User, want.user)
		}
		if alias.Port != want.port {
			t.Fatalf("alias %q: Port = %q, want %q", name, alias.Port, want.port)
		}
		if !sameStrings(alias.IdentityFiles, want.keys) {
			t.Fatalf("alias %q: IdentityFiles = %#v, want %#v", name, alias.IdentityFiles, want.keys)
		}
	}
}

func syntheticGraph(hosts int) *sshconfig.Graph {
	blocks := []sshconfig.HostBlock{
		{
			SourcePath: "synthetic",
			SourceLine: 1,
			Patterns:   []string{"*"},
			Options:    []sshconfig.Option{{Keyword: "port", Values: []string{"22"}}},
		},
		{
			SourcePath: "synthetic",
			SourceLine: 2,
			Patterns:   []string{"web-*"},
			Options:    []sshconfig.Option{{Keyword: "user", Values: []string{"www"}}},
		},
		{
			SourcePath: "synthetic",
			SourceLine: 3,
			Patterns:   []string{"db-[1-9]*"},
			Options:    []sshconfig.Option{{Keyword: "identityfile", Values: []string{"~/.ssh/db_key"}}},
		},
	}
	for i := 0; i < hosts; i++ {
		name := fmt.Sprintf("host-%04d", i)
		blocks = append(blocks, sshconfig.HostBlock{
			SourcePath: "synthetic",
			SourceLine: 100 + i,
			Patterns:   []string{name},
			Options: []sshconfig.Option{
				{Keyword: "hostname", Values: []string{name + ".example.com"}},
				{Keyword: "user", Values: []string{"alice"}},
				{Keyword: "identityfile", Values: []string{"~/.ssh/id_ed25519"}},
			},
		})
	}
	return &sshconfig.Graph{RootPath: "synthetic", Blocks: blocks}
}

func BenchmarkBuild(b *testing.B) {
	for _, hosts := range []int{1000, 5000, 10000} {
		graph := syntheticGraph(hosts)
		b.Run(fmt.Sprintf("hosts=%d", hosts), func(b *testing.B) {
			b.SetBytes(int64(hosts))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				Build(graph, Options{})
			}
		})
	}
}
