package state

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestWriteReadListDeleteProxy(t *testing.T) {
	dir := t.TempDir()
	spec := StoredProxy{
		Name:     "corp-proxy",
		SSHAlias: "bastion",
		Bind:     "127.0.0.1",
		Port:     1080,
	}
	if err := WriteProxy(dir, spec); err != nil {
		t.Fatalf("WriteProxy: %v", err)
	}
	got, err := ReadProxy(dir, spec.Name)
	if err != nil {
		t.Fatalf("ReadProxy: %v", err)
	}
	if got.SSHAlias != "bastion" || got.Port != 1080 || got.StateVersion != StateVersion {
		t.Fatalf("ReadProxy = %#v", got)
	}
	list, err := ListProxies(dir)
	if err != nil {
		t.Fatalf("ListProxies: %v", err)
	}
	if len(list) != 1 || list[0].Name != spec.Name {
		t.Fatalf("ListProxies = %#v", list)
	}
	if err := DeleteProxy(dir, spec.Name); err != nil {
		t.Fatalf("DeleteProxy: %v", err)
	}
	if _, err := ReadProxy(dir, spec.Name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadProxy after delete = %v, want os.ErrNotExist", err)
	}
}

func TestListProxiesSkipsCorruptAndFutureFiles(t *testing.T) {
	dir := t.TempDir()
	if err := WriteProxy(dir, StoredProxy{Name: "good", SSHAlias: "bastion", Port: 1080}); err != nil {
		t.Fatalf("WriteProxy: %v", err)
	}
	corrupt := ProxyPath(dir, "corrupt")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	future := ProxyPath(dir, "future")
	futureBody := `{"name":"future","ssh_alias":"x","port":1081,"state_version":2}`
	if err := os.WriteFile(future, []byte(futureBody), 0o600); err != nil {
		t.Fatalf("write future: %v", err)
	}

	specs, skipped, err := ListProxiesDetailed(dir)
	if err != nil {
		t.Fatalf("ListProxiesDetailed: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "good" {
		t.Fatalf("specs = %#v, want only good", specs)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %#v, want corrupt and future", skipped)
	}
	reasons := map[string]string{}
	for _, skip := range skipped {
		reasons[skip.Path] = skip.Reason
	}
	if reasons[corrupt] == "" || !strings.Contains(reasons[future], "state_version 2") {
		t.Fatalf("skipped = %#v, want corrupt reason and future version reason", skipped)
	}

	plain, err := ListProxies(dir)
	if err != nil {
		t.Fatalf("ListProxies with corrupt sibling: %v", err)
	}
	if len(plain) != 1 || plain[0].Name != "good" {
		t.Fatalf("ListProxies = %#v, want only good", plain)
	}

	if _, err := ReadProxy(dir, "future"); !errors.Is(err, ErrFutureStateVersion) {
		t.Fatalf("ReadProxy(future) = %v, want ErrFutureStateVersion", err)
	}
	if err := WriteProxy(dir, StoredProxy{Name: "future", SSHAlias: "y", Port: 9}); !errors.Is(err, ErrFutureStateVersion) {
		t.Fatalf("WriteProxy over future file = %v, want ErrFutureStateVersion", err)
	}
	data, readErr := os.ReadFile(future)
	if readErr != nil || string(data) != futureBody {
		t.Fatalf("future file changed: %q %v", data, readErr)
	}
}
