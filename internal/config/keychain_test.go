package config_test

import (
	"testing"

	"github.com/zalando/go-keyring"
	"github.com/zhecho/m9s/internal/config"
)

func TestKeychainPassword_roundTrip(t *testing.T) {
	keyring.MockInit()
	if _, err := config.KeychainPassword("work", "smtp"); err == nil {
		t.Fatal("nothing stored yet: want an error")
	}
	if err := config.SetKeychainPassword("work", "smtp", "fromkeychain"); err != nil {
		t.Fatal(err)
	}
	if got, err := config.KeychainPassword("work", "smtp"); err != nil || got != "fromkeychain" {
		t.Errorf("got %q (err %v)", got, err)
	}
}

func TestKeychain_servicesAreSeparate(t *testing.T) {
	keyring.MockInit()
	config.SetKeychainPassword("work", "imap", "i")
	config.SetKeychainPassword("work", "smtp", "s")
	i, _ := config.KeychainPassword("work", "imap")
	s, _ := config.KeychainPassword("work", "smtp")
	if i != "i" || s != "s" {
		t.Errorf("imap %q smtp %q", i, s)
	}
}

func TestDeleteKeychainPassword(t *testing.T) {
	keyring.MockInit()
	config.SetKeychainPassword("work", "imap", "x")
	if err := config.DeleteKeychainPassword("work", "imap"); err != nil {
		t.Fatal(err)
	}
	if _, err := config.KeychainPassword("work", "imap"); err == nil {
		t.Error("password still there after delete")
	}
	if err := config.DeleteKeychainPassword("work", "imap"); err != nil {
		t.Errorf("deleting a missing entry should be a no-op, got %v", err)
	}
}

func TestKeePassXCAssociation_roundTrip(t *testing.T) {
	keyring.MockInit()
	if _, _, err := config.KeePassXCAssociation(); err == nil {
		t.Fatal("nothing stored yet: want an error")
	}
	if err := config.SetKeePassXCAssociation("m9s", "KEY="); err != nil {
		t.Fatal(err)
	}
	id, key, err := config.KeePassXCAssociation()
	if err != nil || id != "m9s" || key != "KEY=" {
		t.Errorf("got %q %q %v, want m9s KEY=", id, key, err)
	}
	if err := config.DeleteKeePassXCAssociation(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := config.KeePassXCAssociation(); err == nil {
		t.Error("still there after delete")
	}
	if err := config.DeleteKeePassXCAssociation(); err != nil {
		t.Errorf("deleting what is not there must not fail: %v", err)
	}
}
