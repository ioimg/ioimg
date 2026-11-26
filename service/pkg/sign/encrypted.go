package sign

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// SourceEncryptionInfo is the HKDF info string for encrypted source URLs. It is
// versioned so a future scheme can derive a different key from the same signing
// secret without invalidating existing tokens.
const SourceEncryptionInfo = "ioimg-source-enc-v1"

// Errors returned by SourceCipher. Callers map them to 400 responses; none of
// them contains key material.
var (
	ErrEncryptedSource  = errors.New("invalid encrypted source")
	ErrUnknownSourceKey = errors.New("unknown encrypted source key id")
	ErrNoSourceKeys     = errors.New("encrypted source requires signing keys")
)

const (
	sourceCipherVersion = 1
	nonceSize           = 12
)

// SourceCipher encrypts and decrypts source URLs with AES-256-GCM. A separate
// key is derived from every configured signing secret with HKDF-SHA256, so the
// signing secret is never used directly as an encryption key.
type SourceCipher struct {
	keys []sourceKey
}

type sourceKey struct {
	id   string
	aead cipher.AEAD
}

// NewSourceCipher derives one AES-256-GCM key per signing key. Every key is
// tried for decryption, which is what makes rotation possible: an old token
// keeps working until its key is removed from IOIMG_SIGNING_KEYS.
func NewSourceCipher(keys []Key) (*SourceCipher, error) {
	c := &SourceCipher{}
	for _, k := range keys {
		if k.ID == "" {
			return nil, fmt.Errorf("encrypted source key id cannot be empty")
		}
		if len(k.ID) > 255 {
			return nil, fmt.Errorf("encrypted source key id %q is too long", k.ID)
		}
		derived, err := hkdf.Key(sha256.New, k.Secret, nil, SourceEncryptionInfo, 32)
		if err != nil {
			return nil, fmt.Errorf("derive source key %q: %w", k.ID, err)
		}
		block, err := aes.NewCipher(derived)
		if err != nil {
			return nil, fmt.Errorf("source key %q: %w", k.ID, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("source key %q: %w", k.ID, err)
		}
		c.keys = append(c.keys, sourceKey{id: k.ID, aead: aead})
	}
	return c, nil
}

// Encrypt returns an es_ token for plain. kid selects the key; an empty kid uses
// the first configured key.
func (c *SourceCipher) Encrypt(plain, kid string) (string, error) {
	if len(c.keys) == 0 {
		return "", ErrNoSourceKeys
	}
	key := &c.keys[0]
	if kid != "" {
		found := false
		for i := range c.keys {
			if c.keys[i].id == kid {
				key = &c.keys[i]
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%w: %q", ErrUnknownSourceKey, kid)
		}
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := key.aead.Seal(nil, nonce, []byte(plain), nil)

	payload := make([]byte, 0, 2+len(key.id)+nonceSize+len(sealed))
	payload = append(payload, sourceCipherVersion, byte(len(key.id)))
	payload = append(payload, key.id...)
	payload = append(payload, nonce...)
	payload = append(payload, sealed...)
	return "es_" + base64.RawURLEncoding.EncodeToString(payload), nil
}

// Decrypt opens an es_ token and reports which key id succeeded. The key id is
// only used for the debug response header; it is never part of the source URL
// seen by the origin.
func (c *SourceCipher) Decrypt(token string) (plain, kid string, err error) {
	if len(c.keys) == 0 {
		return "", "", ErrNoSourceKeys
	}
	if len(token) < 3 || token[:3] != "es_" {
		return "", "", ErrEncryptedSource
	}
	raw, err := base64.RawURLEncoding.DecodeString(token[3:])
	if err != nil {
		return "", "", ErrEncryptedSource
	}
	// version, kid length, kid, nonce, ciphertext+tag
	if len(raw) < 2 || raw[0] != sourceCipherVersion {
		return "", "", ErrEncryptedSource
	}
	idLen := int(raw[1])
	if idLen == 0 || len(raw) < 2+idLen+nonceSize+1 {
		return "", "", ErrEncryptedSource
	}
	id := string(raw[2 : 2+idLen])
	nonce := raw[2+idLen : 2+idLen+nonceSize]
	sealed := raw[2+idLen+nonceSize:]

	for i := range c.keys {
		if c.keys[i].id != id {
			continue
		}
		out, err := c.keys[i].aead.Open(nil, nonce, sealed, nil)
		if err != nil {
			return "", "", ErrEncryptedSource
		}
		return string(out), id, nil
	}
	// The id is public metadata (it is also returned by signed URLs), so naming
	// it in the error is safe and makes rotation mistakes obvious.
	return "", "", fmt.Errorf("%w: %q", ErrUnknownSourceKey, id)
}
