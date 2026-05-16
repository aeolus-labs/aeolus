// Package secrets stores upstream env-var secrets in the system keychain
// (macOS Keychain, Windows Credential Manager, libsecret on Linux) so that
// aeolus.yaml never contains plaintext credentials.
package secrets

import (
	"errors"

	"github.com/zalando/go-keyring"
)

const service = "aeolus"

// ErrNotFound is returned when a secret name is not present in the keychain.
var ErrNotFound = errors.New("secret not found")

// Get returns the secret stored under name, or ErrNotFound if missing.
func Get(name string) (string, error) {
	v, err := keyring.Get(service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

// Set writes value under name, overwriting any existing secret.
func Set(name, value string) error {
	return keyring.Set(service, name, value)
}

// Delete removes the secret. Missing entries are a no-op.
func Delete(name string) error {
	err := keyring.Delete(service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
