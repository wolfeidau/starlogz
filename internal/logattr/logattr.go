package logattr

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
)

func ObscureString(key, value string) slog.Attr {
	hash := sha256.Sum256([]byte(value))
	hashStr := hex.EncodeToString(hash[:4])
	return slog.String(key, hashStr)
}
