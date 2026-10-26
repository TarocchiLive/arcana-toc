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
	if err := checkCredentialFile(info); err != nil {
		t.Fatalf("credential permissions: %v", err)
	}
}

func TestCredentialReplacementWhileLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qqmusic.json")
	lock, err := LockCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if second, err := LockCredential(path); err == nil {
		_ = second.Close()
		t.Fatal("concurrent credential writer acquired lock")
	}
	for _, key := range []string{"old-key", "new-key"} {
		if err := WriteCredential(path, Credential{MusicID: "42", MusicKey: key, LoginType: 2}); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := ReadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if restored.MusicKey != "new-key" {
		t.Fatalf("replacement was not persisted: %q", restored.MusicKey)
	}
}
