// Package secrets encrypts credentials that must be recoverable at use time
// (SMTP passwords — the server has to present them to the mail server, so a
// one-way hash is impossible). Values are sealed with AES-256-GCM under a key
// kept OUTSIDE the database: by default a 0600 file in the config dir,
// overridable with the BOOKY_SECRET_KEY environment variable. Backups archive
// only booky.db, so a downloaded backup alone can never yield a credential.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const keyFile = "secret.key"

// Keeper seals and opens credential values with a single symmetric key.
type Keeper struct {
	aead cipher.AEAD
}

// Load resolves the key: BOOKY_SECRET_KEY (64 hex chars) when set, else the
// config dir's key file, generated on first use.
func Load(configDir string) (*Keeper, error) {
	if env := strings.TrimSpace(os.Getenv("BOOKY_SECRET_KEY")); env != "" {
		key, err := hex.DecodeString(env)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("BOOKY_SECRET_KEY must be 64 hex characters (32 bytes)")
		}
		return newKeeper(key)
	}
	path := filepath.Join(configDir, keyFile)
	raw, err := os.ReadFile(path) //nolint:gosec // fixed name inside the config dir
	if err == nil {
		key, derr := hex.DecodeString(strings.TrimSpace(string(raw)))
		if derr != nil || len(key) != 32 {
			return nil, fmt.Errorf("%s is corrupt — restore it or delete it to rotate (stored credentials must then be re-entered)", path)
		}
		return newKeeper(key)
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return newKeeper(key)
}

func newKeeper(key []byte) (*Keeper, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Keeper{aead: aead}, nil
}

// Seal encrypts a value; the random nonce is prefixed to the ciphertext.
func (k *Keeper) Seal(value string) ([]byte, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return k.aead.Seal(nonce, nonce, []byte(value), nil), nil
}

// Open decrypts a sealed value.
func (k *Keeper) Open(ct []byte) (string, error) {
	if len(ct) < k.aead.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, body := ct[:k.aead.NonceSize()], ct[k.aead.NonceSize():]
	plain, err := k.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt failed — was the secret key file replaced?")
	}
	return string(plain), nil
}
