package server

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"hash"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

// hashsumMatches verifies an hs_ digest in constant time. The digest type was
// validated at parse time, so an unknown type can only mean "not set".
func hashsumMatches(h ops.Hashsum, body []byte) bool {
	var fn func() hash.Hash
	switch h.Type {
	case "md5":
		fn = md5.New
	case "sha1":
		fn = sha1.New
	case "sha256":
		fn = sha256.New
	case "sha512":
		fn = sha512.New
	default:
		return true
	}
	d := fn()
	d.Write(body)
	got := hex.EncodeToString(d.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.Hex)) == 1
}
