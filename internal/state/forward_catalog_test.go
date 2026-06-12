package state

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWriteReadListDeleteForward(t *testing.T) {
	dir := t.TempDir()
	spec := StoredForward{
		Name:       "pngwin-pg-tunnel",
		SSHAlias:   "pgbox",
		LocalBind:  "127.0.0.1",
		LocalPort:  5432,
		RemoteHost: "127.0.0.1",
		RemotePort: 5432,
		Through:    "bastion",
	}
	if err := WriteForward(dir, spec); err != nil {
		t.Fatalf("WriteForward: %v", err)
	}

	got, err := ReadForward(dir, spec.Name)
	if err != nil {
		t.Fatalf("ReadForward: %v", err)
	}
	if got.Name != spec.Name || got.SSHAlias != spec.SSHAlias {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, spec)
	}
	if got.LocalPort != 5432 || got.RemotePort != 5432 || got.Through != "bastion" {
		t.Fatalf("forward spec fields lost: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not set: %+v", got)
	}
	if got.StateVersion != StateVersion {
		t.Fatalf("StateVersion = %d, want %d", got.StateVersion, StateVersion)
	}

	// Update preserves CreatedAt but bumps UpdatedAt.
	originalCreated := got.CreatedAt
	time.Sleep(2 * time.Millisecond)
	spec.LocalPort = 5433
	if err := WriteForward(dir, spec); err != nil {
		t.Fatalf("update WriteForward: %v", err)
	}
	updated, _ := ReadForward(dir, spec.Name)
	if !updated.CreatedAt.Equal(originalCreated) {
		t.Fatalf("CreatedAt changed on update: %v vs %v", updated.CreatedAt, originalCreated)
	}
	if !updated.UpdatedAt.After(updated.CreatedAt) {
		t.Fatalf("UpdatedAt did not advance: %v vs %v", updated.UpdatedAt, updated.CreatedAt)
	}
	if updated.LocalPort != 5433 {
		t.Fatalf("local port did not update: %d", updated.LocalPort)
	}

	// List returns this entry plus a second one, sorted by name.
	second := StoredForward{Name: "another", SSHAlias: "x", LocalPort: 1, RemoteHost: "h", RemotePort: 2}
	if err := WriteForward(dir, second); err != nil {
		t.Fatalf("WriteForward second: %v", err)
	}
	list, err := ListForwards(dir)
	if err != nil {
		t.Fatalf("ListForwards: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List length = %d, want 2", len(list))
	}
	if list[0].Name != "another" || list[1].Name != "pngwin-pg-tunnel" {
		t.Fatalf("List not sorted by name: %+v", list)
	}

	// Delete is idempotent.
	if err := DeleteForward(dir, "another"); err != nil {
		t.Fatalf("DeleteForward: %v", err)
	}
	if err := DeleteForward(dir, "another"); err != nil {
		t.Fatalf("DeleteForward (second time) should be idempotent: %v", err)
	}
	if _, err := ReadForward(dir, "another"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadForward after Delete = %v, want os.ErrNotExist", err)
	}
}

func TestListForwardsMissingDirIsEmpty(t *testing.T) {
	dir := t.TempDir()
	list, err := ListForwards(dir)
	if err != nil {
		t.Fatalf("ListForwards: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list = %v, want empty", list)
	}
}

func TestValidateForwardName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"pngwin-pg-tunnel", false},
		{"foo_bar", false},
		{"foo.bar", false},
		{"", true},
		{" leading-space", true},
		{"trailing-space ", true},
		{"has space", true},
		{".dotfile", true},
		{"slash/in/name", true},
		{"back\\slash", true},
		{"with\nnewline", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateForwardName(c.name)
			if c.wantErr && err == nil {
				t.Fatalf("expected error for %q", c.name)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", c.name, err)
			}
		})
	}
}

func TestReadForwardCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := ForwardPath(dir, "bad")
	if err := os.MkdirAll(ForwardPath(dir, "_")[:len(ForwardPath(dir, "_"))-len("/_.json")], 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	if _, err := ReadForward(dir, "bad"); err == nil {
		t.Fatalf("expected error on corrupt JSON")
	}
}

func TestListForwardsSkipsCorruptAndFutureFiles(t *testing.T) {
	dir := t.TempDir()
	good := StoredForward{Name: "good", SSHAlias: "box", LocalPort: 1, RemoteHost: "h", RemotePort: 2}
	if err := WriteForward(dir, good); err != nil {
		t.Fatalf("WriteForward: %v", err)
	}
	corrupt := ForwardPath(dir, "corrupt")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	future := ForwardPath(dir, "future")
	if err := os.WriteFile(future, []byte(`{"name":"future","ssh_alias":"x","local_port":1,"remote_host":"h","remote_port":2,"state_version":2}`), 0o600); err != nil {
		t.Fatalf("write future: %v", err)
	}

	specs, skipped, err := ListForwardsDetailed(dir)
	if err != nil {
		t.Fatalf("ListForwardsDetailed: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "good" {
		t.Fatalf("specs = %#v, want only good", specs)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %#v, want corrupt and future", skipped)
	}
	paths := map[string]string{}
	for _, skip := range skipped {
		paths[skip.Path] = skip.Reason
	}
	if paths[corrupt] == "" {
		t.Fatalf("corrupt file not surfaced: %#v", skipped)
	}
	if !strings.Contains(paths[future], "state_version 2") {
		t.Fatalf("future file reason = %q, want state_version mention", paths[future])
	}

	plain, err := ListForwards(dir)
	if err != nil {
		t.Fatalf("ListForwards with corrupt sibling: %v", err)
	}
	if len(plain) != 1 || plain[0].Name != "good" {
		t.Fatalf("ListForwards = %#v, want only good", plain)
	}
}

func TestForwardFutureStateVersionReadAndWriteGate(t *testing.T) {
	dir := t.TempDir()
	future := ForwardPath(dir, "future")
	body := `{"name":"future","ssh_alias":"x","local_port":1,"remote_host":"h","remote_port":2,"state_version":2}`
	if err := os.MkdirAll(filepath.Dir(future), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(future, []byte(body), 0o600); err != nil {
		t.Fatalf("write future: %v", err)
	}
	if _, err := ReadForward(dir, "future"); !errors.Is(err, ErrFutureStateVersion) {
		t.Fatalf("ReadForward(future) = %v, want ErrFutureStateVersion", err)
	}
	err := WriteForward(dir, StoredForward{Name: "future", SSHAlias: "y", LocalPort: 3, RemoteHost: "h2", RemotePort: 4})
	if !errors.Is(err, ErrFutureStateVersion) {
		t.Fatalf("WriteForward over future file = %v, want ErrFutureStateVersion", err)
	}
	data, readErr := os.ReadFile(future)
	if readErr != nil || string(data) != body {
		t.Fatalf("future file changed: %q %v", data, readErr)
	}
}

func TestForwardSpecRoundTripJSON(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Add(-1 * time.Hour)
	spec := StoredForward{
		Name:           "redis-tunnel",
		SSHAlias:       "redisbox",
		LocalPort:      6379,
		RemoteHost:     "127.0.0.1",
		RemotePort:     6379,
		Description:    "main redis tunnel",
		LastLaunchedAt: &now,
	}
	if err := WriteForward(dir, spec); err != nil {
		t.Fatalf("WriteForward: %v", err)
	}
	got, err := ReadForward(dir, "redis-tunnel")
	if err != nil {
		t.Fatalf("ReadForward: %v", err)
	}
	want := []string{"redis-tunnel", "redisbox", "127.0.0.1", "main redis tunnel"}
	for _, w := range want {
		if !reflect.DeepEqual(got.Name, "redis-tunnel") {
			t.Fatalf("name round-trip: %q", got.Name)
		}
		_ = w
	}
	if got.LastLaunchedAt == nil || got.LastLaunchedAt.IsZero() {
		t.Fatalf("LastLaunchedAt not preserved: %+v", got.LastLaunchedAt)
	}
}
