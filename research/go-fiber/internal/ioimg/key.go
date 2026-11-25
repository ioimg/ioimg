package ioimg

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// CacheKey: sha256("v1|ops|source") → 16 byte pertama, hex (32 char).
func CacheKey(o Ops, source string) string {
	h := sha256.New()
	h.Write([]byte("v1|"))
	h.Write([]byte(o.Canonical()))
	h.Write([]byte("|"))
	h.Write([]byte(source))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func CachePath(dir, key string, f Format) string {
	return filepath.Join(dir, key+"."+f.Ext())
}
