package sign

import (
	"errors"
	"strings"
	"testing"
)

func TestSourceCipherRoundTripAndRotation(t *testing.T) {
	keys := []Key{{ID: "new", Secret: []byte("new-secret")}, {ID: "old", Secret: []byte("old-secret")}}
	c, err := NewSourceCipher(keys)
	if err != nil {
		t.Fatal(err)
	}
	source := "https://cdn.example.com/a.jpg?token=1"
	for _, kid := range []string{"new", "old"} {
		token, err := c.Encrypt(source, kid)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(token, "es_") {
			t.Fatalf("token missing es_ prefix: %s", token)
		}
		got, used, err := c.Decrypt(token)
		if err != nil {
			t.Fatal(err)
		}
		if got != source || used != kid {
			t.Fatalf("round trip = %q %q, want %q %q", got, used, source, kid)
		}
	}
}

func TestSourceCipherRejectsWrongKeyAndTamper(t *testing.T) {
	c, _ := NewSourceCipher([]Key{{ID: "k1", Secret: []byte("secret")}})
	token, err := c.Encrypt("https://example.com/a.jpg", "k1")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := NewSourceCipher([]Key{{ID: "k2", Secret: []byte("other")}})
	if _, _, err := other.Decrypt(token); !errors.Is(err, ErrUnknownSourceKey) {
		t.Fatalf("want ErrUnknownSourceKey, got %v", err)
	}
	tampered := token[:len(token)-2] + "AA"
	if _, _, err := c.Decrypt(tampered); !errors.Is(err, ErrEncryptedSource) {
		t.Fatalf("want ErrEncryptedSource, got %v", err)
	}
}
