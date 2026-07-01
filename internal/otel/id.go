package otel

import (
	"crypto/sha256"
	"encoding/hex"
)

func DeriveTraceID(seed string) string {
	return deriveHex("trace:"+seed, 16)
}

func DeriveSpanID(seed string) string {
	return deriveHex("span:"+seed, 8)
}

func deriveHex(seed string, n int) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:n])
}
