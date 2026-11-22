package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Plugin configs hold passwords and keys (the ista password, the Tuya
// secret and local keys). With a key (UseKey) a config is stored encrypted
// with AES-256-GCM as "enc:v1:<base64 of nonce and ciphertext>". This keeps
// them out of the database file and its .bak copies, e.g. in a backup;
// whoever can read the whole data directory can read the key too.

const sealPrefix = "enc:v1:"

// LoadOrCreateKey reads a 32-byte key from path, or creates one (owner-only)
// when the file does not exist.
func LoadOrCreateKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("store: %s is geen geldige sleutel (%d bytes)", path, len(b))
		}
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return nil, err
	}
	return key, f.Close()
}

// UseKey turns on encryption of plugin configs and encrypts those still
// stored in plain text.
func (s *Store) UseKey(ctx context.Context, key []byte) error {
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	s.aead = aead
	for _, table := range []string{"plugins", "plugin_devices"} {
		rows, err := s.db.QueryContext(ctx, "SELECT id, config FROM "+table)
		if err != nil {
			return err
		}
		plain := map[string]string{}
		for rows.Next() {
			var id, cfg string
			if err := rows.Scan(&id, &cfg); err != nil {
				rows.Close()
				return err
			}
			if !strings.HasPrefix(cfg, sealPrefix) {
				plain[id] = cfg
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for id, cfg := range plain {
			if _, err := s.db.ExecContext(ctx, "UPDATE "+table+" SET config = ? WHERE id = ?", s.sealConfig(unmarshalConfig(cfg)), id); err != nil {
				return err
			}
		}
	}
	return nil
}

// sealConfig encodes a config for storage, encrypted when a key is set.
func (s *Store) sealConfig(m map[string]string) string {
	plain := marshalConfig(m)
	if s.aead == nil || plain == "{}" {
		return plain
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return plain // never happens; plain text beats losing the config
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(plain), nil)
	return sealPrefix + base64.StdEncoding.EncodeToString(sealed)
}

// openConfig decodes a stored config. An encrypted config that cannot be
// opened (no key, another key) comes back empty: the user has to enter its
// secrets again.
func (s *Store) openConfig(stored string) map[string]string {
	if !strings.HasPrefix(stored, sealPrefix) {
		return unmarshalConfig(stored)
	}
	if s.aead == nil {
		return map[string]string{}
	}
	b, err := base64.StdEncoding.DecodeString(stored[len(sealPrefix):])
	n := s.aead.NonceSize()
	if err != nil || len(b) < n {
		return map[string]string{}
	}
	plain, err := s.aead.Open(nil, b[:n], b[n:], nil)
	if err != nil {
		return map[string]string{}
	}
	return unmarshalConfig(string(plain))
}
