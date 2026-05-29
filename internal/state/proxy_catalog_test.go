package state

import (
	"errors"
	"os"
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
