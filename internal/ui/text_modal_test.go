package ui

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

// A secret prompt must never render the typed value — only a mask.
func TestTextPromptSecretMasksValue(t *testing.T) {
	secret := newTextPromptModel(TextPromptOptions{
		Title:   "Encrypted key",
		Label:   "passphrase",
		Initial: "hunter2",
		Secret:  true,
	}, termstyle.Theme{})
	content := secret.View().Content
	if strings.Contains(content, "hunter2") {
		t.Fatalf("secret prompt leaked the passphrase into the rendered view:\n%s", content)
	}
	if !strings.Contains(content, "•") {
		t.Fatalf("secret prompt did not mask the value with bullets:\n%s", content)
	}
}

// A normal prompt shows what was typed (regression guard so the mask only
// applies when Secret is set).
func TestTextPromptPlainShowsValue(t *testing.T) {
	plain := newTextPromptModel(TextPromptOptions{
		Title:   "Destination name",
		Label:   "name",
		Initial: "id_work",
	}, termstyle.Theme{})
	content := plain.View().Content
	if !strings.Contains(content, "id_work") {
		t.Fatalf("plain prompt should show the typed value:\n%s", content)
	}
	if strings.Contains(content, "•") {
		t.Fatalf("plain prompt should not mask:\n%s", content)
	}
}
