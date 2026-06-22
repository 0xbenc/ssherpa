package fuzzy

import (
	"reflect"
	"testing"
)

func TestMatchPositionsAndOK(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		candidate string
		wantOK    bool
		wantPos   []int
	}{
		{"boundary", "db", "prod-db1", true, []int{5, 6}},
		{"empty query", "", "anything", true, nil},
		{"no match", "xyz", "prod-db1", false, nil},
		{"exact", "abc", "abc", true, []int{0, 1, 2}},
		{"longer than candidate", "abcd", "abc", false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, ok := Match(tc.query, tc.candidate)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && !reflect.DeepEqual(res.Positions, tc.wantPos) {
				t.Fatalf("positions = %v, want %v", res.Positions, tc.wantPos)
			}
		})
	}
}

func TestSmartCase(t *testing.T) {
	if _, ok := Match("db", "prod-DB1"); !ok {
		t.Fatal("lowercase query should match uppercase case-insensitively")
	}
	if _, ok := Match("DB", "prod-db1"); ok {
		t.Fatal("uppercase query must not match lowercase (smart-case)")
	}
}

func TestRelevanceGate(t *testing.T) {
	// A scattered subsequence (letters spread mid-word with large gaps) is not
	// relevant; a boundary/prefix match is.
	scattered, _ := Match("redis", "occpw internet midwest0 wifi password")
	if Relevant(scattered, len("redis")) {
		t.Fatalf("scattered match (score %d) should not be relevant", scattered.Score)
	}
	strong, _ := Match("prod", "prod-db1")
	if !Relevant(strong, len("prod")) {
		t.Fatalf("prefix match (score %d) should be relevant", strong.Score)
	}
	if !Relevant(Result{}, 0) {
		t.Fatal("empty query should be relevant")
	}
}

func TestBoundaryOutranksMidWord(t *testing.T) {
	boundary, _ := Match("db", "prod-db1") // d at a "-" boundary
	mid, _ := Match("db", "xadbz")         // d mid-word
	if boundary.Score <= mid.Score {
		t.Fatalf("boundary score %d should exceed mid-word score %d", boundary.Score, mid.Score)
	}
}
