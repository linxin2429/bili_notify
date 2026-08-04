package web

import (
	"path/filepath"
	"testing"

	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/vault"
)

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password was rejected")
	}
	if verifyPassword(hash, "wrong password") {
		t.Fatal("wrong password was accepted")
	}
}

func TestShortPasswordRejected(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password was accepted")
	}
}

func TestAdministratorInitializationPersists(t *testing.T) {
	v, err := vault.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth, setupCode, err := newAuthenticator(store)
	if err != nil {
		t.Fatal(err)
	}
	if setupCode == "" {
		t.Fatal("missing setup code")
	}
	if err := auth.initialize("WRONG", "correct horse battery staple"); err == nil {
		t.Fatal("wrong setup code was accepted")
	}
	if err := auth.initialize(setupCode, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if !auth.authenticate("correct horse battery staple") {
		t.Fatal("initialized password was rejected")
	}
	reopened, nextCode, err := newAuthenticator(store)
	if err != nil {
		t.Fatal(err)
	}
	if nextCode != "" || !reopened.authenticate("correct horse battery staple") {
		t.Fatal("administrator state was not persisted")
	}
}
