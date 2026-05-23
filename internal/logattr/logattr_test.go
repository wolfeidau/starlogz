package logattr

import (
	"log/slog"
	"testing"
)

func TestObscureString(t *testing.T) {
	attr := ObscureString("token", "secret-value")

	if attr.Key != "token" {
		t.Errorf("key = %q, want %q", attr.Key, "token")
	}

	got := attr.Value.String()

	if len(got) != 8 {
		t.Errorf("hash length = %d, want 16 hex chars", len(got))
	}

	for _, c := range got {
		if ('0' > c || c > '9') && ('a' > c || c > 'f') {
			t.Errorf("non-hex character %q in output %q", c, got)
		}
	}
}

func TestObscureString_deterministic(t *testing.T) {
	a := ObscureString("k", "same-input")
	b := ObscureString("k", "same-input")

	if a.Value.String() != b.Value.String() {
		t.Errorf("same input produced different hashes: %q vs %q", a.Value.String(), b.Value.String())
	}
}

func TestObscureString_distinctInputs(t *testing.T) {
	a := ObscureString("k", "token-one")
	b := ObscureString("k", "token-two")

	if a.Value.String() == b.Value.String() {
		t.Errorf("different inputs produced same hash %q", a.Value.String())
	}
}

func TestObscureString_returnsAttr(t *testing.T) {
	attr := ObscureString("my_key", "value")

	if attr.Key != "my_key" {
		t.Errorf("unexpected key %q", attr.Key)
	}
	if attr.Value.Kind() != slog.KindString {
		t.Errorf("expected string kind, got %v", attr.Value.Kind())
	}
}
