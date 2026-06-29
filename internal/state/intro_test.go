package state

import (
	"os"
	"testing"
)

func TestReadIntroMissingReturnsZeroValue(t *testing.T) {
	dir := t.TempDir()
	got, err := ReadIntro(dir)
	if err != nil {
		t.Fatalf("ReadIntro on missing file: %v", err)
	}
	if (got != IntroState{}) {
		t.Fatalf("ReadIntro on missing file = %#v, want zero value", got)
	}
}

func TestIntroWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := IntroState{SchemaVersion: IntroSchemaVersion, LastSeenIntroVersion: "v1.2.3"}
	if err := WriteIntro(dir, want); err != nil {
		t.Fatalf("WriteIntro: %v", err)
	}
	got, err := ReadIntro(dir)
	if err != nil {
		t.Fatalf("ReadIntro: %v", err)
	}
	if got != want {
		t.Fatalf("round-tripped IntroState = %#v, want %#v", got, want)
	}
}

func TestWriteIntroStampsSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	// SchemaVersion left at zero must be stamped to the current version.
	if err := WriteIntro(dir, IntroState{LastSeenIntroVersion: "dev"}); err != nil {
		t.Fatalf("WriteIntro: %v", err)
	}
	got, err := ReadIntro(dir)
	if err != nil {
		t.Fatalf("ReadIntro: %v", err)
	}
	if got.SchemaVersion != IntroSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", got.SchemaVersion, IntroSchemaVersion)
	}
	if got.LastSeenIntroVersion != "dev" {
		t.Fatalf("LastSeenIntroVersion = %q, want %q", got.LastSeenIntroVersion, "dev")
	}
}

func TestWriteIntroUsesOwnerOnlyMode(t *testing.T) {
	dir := t.TempDir()
	if err := WriteIntro(dir, IntroState{LastSeenIntroVersion: "v0.1.0"}); err != nil {
		t.Fatalf("WriteIntro: %v", err)
	}
	info, err := os.Stat(IntroPath(dir))
	if err != nil {
		t.Fatalf("stat intro.json: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("intro.json mode = %o, want 0600", perm)
	}
}

func TestReadIntroCorruptFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(IntroPath(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	if _, err := ReadIntro(dir); err == nil {
		t.Fatalf("ReadIntro on corrupt file = nil error, want error")
	}
}
