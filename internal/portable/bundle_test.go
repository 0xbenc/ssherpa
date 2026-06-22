package portable

import (
	"errors"
	"testing"

	"github.com/0xbenc/ssherpa/internal/sshconfig"
	"github.com/0xbenc/ssherpa/internal/state"
)

func TestBundleRoundTrip(t *testing.T) {
	in := Bundle{
		Aliases: []AliasEntry{
			{Alias: "prod", HostName: "prod.example.com", User: "alice", Port: "2222", IdentityFile: "~/.ssh/id", IdentitiesOnly: true, ProxyJump: "bastion"},
			{Alias: "pwbox", HostName: "pwbox.example.com", ForcePassword: true},
		},
		Forwards: []state.StoredForward{
			{Name: "db", SSHAlias: "prod", LocalPort: 5432, RemoteHost: "db.internal", RemotePort: 5432},
		},
		Proxies: []state.StoredProxy{
			{Name: "socks", SSHAlias: "prod", Port: 1080},
		},
	}

	data, err := Marshal(in, "2026-06-21T00:00:00Z")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	out, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.SchemaVersion != BundleSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", out.SchemaVersion, BundleSchemaVersion)
	}
	if out.ExportedAt != "2026-06-21T00:00:00Z" {
		t.Fatalf("ExportedAt = %q, want stamped", out.ExportedAt)
	}
	if len(out.Aliases) != 2 || len(out.Forwards) != 1 || len(out.Proxies) != 1 {
		t.Fatalf("counts = %d/%d/%d, want 2/1/1", len(out.Aliases), len(out.Forwards), len(out.Proxies))
	}
	if out.Aliases[0] != in.Aliases[0] {
		t.Fatalf("alias[0] = %#v, want %#v", out.Aliases[0], in.Aliases[0])
	}
	if !out.Aliases[1].ForcePassword {
		t.Fatalf("force_password dropped in round-trip: %#v", out.Aliases[1])
	}
}

func TestAliasEntrySpecRoundTrip(t *testing.T) {
	specs := []sshconfig.AliasSpec{
		{Alias: "a", HostName: "h", User: "u", Port: "22", IdentityFile: "~/.ssh/id", IdentitiesOnly: true, ProxyJump: "j"},
		{Alias: "b", HostName: "h2", ForcePassword: true},
		{Alias: "c", HostName: "h3"},
	}
	for _, spec := range specs {
		if got := AliasEntryFromSpec(spec).ToSpec(); got != spec {
			t.Fatalf("round-trip mismatch: got %#v, want %#v", got, spec)
		}
	}
}

func TestUnmarshalRejectsFutureVersion(t *testing.T) {
	data := []byte(`{"schema_version": 999}`)
	if _, err := Unmarshal(data); !errors.Is(err, ErrFutureBundleVersion) {
		t.Fatalf("Unmarshal future = %v, want ErrFutureBundleVersion", err)
	}
}

func TestUnmarshalRejectsMissingVersion(t *testing.T) {
	if _, err := Unmarshal([]byte(`{}`)); err == nil {
		t.Fatalf("Unmarshal without version = nil error, want failure")
	}
}

func TestUnmarshalRejectsMalformed(t *testing.T) {
	if _, err := Unmarshal([]byte(`{not json`)); err == nil {
		t.Fatalf("Unmarshal malformed = nil error, want failure")
	}
}

func TestBundleIsEmpty(t *testing.T) {
	if !(Bundle{}).IsEmpty() {
		t.Fatalf("zero bundle should be empty")
	}
	if (Bundle{Aliases: []AliasEntry{{Alias: "x"}}}).IsEmpty() {
		t.Fatalf("bundle with an alias should not be empty")
	}
}
