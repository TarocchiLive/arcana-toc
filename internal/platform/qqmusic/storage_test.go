package qqmusic

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qqmusic.json")
	original := Credential{MusicID: "42", MusicKey: "fixture-key", LoginType: 2}
	if err := WriteCredential(path, original); err != nil {
		t.Fatal(err)
	}
	restored, err := ReadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if restored.MusicID != original.MusicID || restored.MusicKey != original.MusicKey || restored.LoginType != original.LoginType {
		t.Fatal("saved credential did not survive reload")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential permissions = %o", info.Mode().Perm())
	}
}
