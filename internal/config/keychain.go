package config

import (
	"encoding/json"
	"errors"

	"github.com/zalando/go-keyring"
)

// KeychainService is the service name m9s uses for entries in the OS keychain
// (macOS Keychain, Linux Secret Service). Each entry's account is
// "<account name>/<imap|smtp>".
const KeychainService = "m9s"

func keychainUser(accountName, service string) string {
	return accountName + "/" + service
}

// SetKeychainPassword stores (or replaces) a password in the OS keychain.
func SetKeychainPassword(accountName, service, password string) error {
	return keyring.Set(KeychainService, keychainUser(accountName, service), password)
}

// DeleteKeychainPassword removes a password from the OS keychain. A missing
// entry is not an error.
func DeleteKeychainPassword(accountName, service string) error {
	err := keyring.Delete(KeychainService, keychainUser(accountName, service))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// KeychainPassword returns the password `m9s login` stored for the account's
// service, or an error if there is none.
func KeychainPassword(accountName, service string) (string, error) {
	return keyring.Get(KeychainService, keychainUser(accountName, service))
}

// keepassxcAssociationUser is the keychain entry that remembers the
// connection the user approved in KeePassXC.
const keepassxcAssociationUser = "keepassxc/association"

type keepassxcAssociation struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// KeePassXCAssociation returns the remembered KeePassXC approval: the name
// the user gave the connection and the key it was approved for.
func KeePassXCAssociation() (id, key string, err error) {
	raw, err := keyring.Get(KeychainService, keepassxcAssociationUser)
	if err != nil {
		return "", "", err
	}
	var a keepassxcAssociation
	if err := json.Unmarshal([]byte(raw), &a); err != nil || a.ID == "" || a.Key == "" {
		return "", "", errors.New("the remembered KeePassXC approval is unreadable")
	}
	return a.ID, a.Key, nil
}

// SetKeePassXCAssociation remembers a KeePassXC approval.
func SetKeePassXCAssociation(id, key string) error {
	raw, err := json.Marshal(keepassxcAssociation{ID: id, Key: key})
	if err != nil {
		return err
	}
	return keyring.Set(KeychainService, keepassxcAssociationUser, string(raw))
}

// DeleteKeePassXCAssociation forgets the approval; it is not an error if
// there was none.
func DeleteKeePassXCAssociation() error {
	err := keyring.Delete(KeychainService, keepassxcAssociationUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
