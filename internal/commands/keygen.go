package commands

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/lestrrat-go/jwx/v4/jwk"
)

type KeyGenCmd struct {
	Output string `help:"The output path of the generated key."`
}

func (c *KeyGenCmd) Run(_ context.Context, globals *Globals) error {
	privateKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate key: %w", err)
	}

	pubKeyBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return fmt.Errorf("failed to marshal public key: %w", err)
	}

	fingerprint := sha256.Sum256(pubKeyBytes)

	globals.Logger.Info("generated key", slog.String("fingerprint", hex.EncodeToString(fingerprint[:])))

	jwkKey, err := jwk.Import[jwk.Key](privateKey)
	if err != nil {
		return fmt.Errorf("failed to import key: %w", err)
	}

	jwkRaw, err := json.Marshal(jwkKey)
	if err != nil {
		return fmt.Errorf("failed to marshal json web key: %w", err)
	}

	if c.Output == "" {
		fmt.Println(string(jwkRaw))

		return nil
	}

	err = os.WriteFile(c.Output, jwkRaw, 0o600)
	if err != nil {
		return fmt.Errorf("failed to save json web key: %w", err)
	}

	return nil
}
