package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func itoa(n int) string                   { return strconv.Itoa(n) }

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
