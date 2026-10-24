package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// BANSOKO_UPDATE_PUBLIC_KEY_PATH when set, falling back to the built-in image path
// then the repository checkout (native development). The file format matches
// cmd/bansoko-release readKey: hex or raw-base64 bytes of an ed25519 public key.
func loadEmbeddedUpdatePublicKey() (ed25519.PublicKey, error) {
	candidates := []string{
		os.Getenv("BANSOKO_UPDATE_PUBLIC_KEY_PATH"),
		"/app/public.key",
		"deployment/public.key",
	}
	for _, path := range candidates {
		if strings.TrimSpace(path) == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(data))
		if decoded, decodeErr := hex.DecodeString(value); decodeErr == nil && len(decoded) == ed25519.PublicKeySize {
			return ed25519.PublicKey(decoded), nil
		}
		if decoded, decodeErr := base64.RawStdEncoding.DecodeString(value); decodeErr == nil && len(decoded) == ed25519.PublicKeySize {
			return ed25519.PublicKey(decoded), nil
		}
		if len(data) == ed25519.PublicKeySize {
			return ed25519.PublicKey(data), nil
		}
		return nil, fmt.Errorf("public key at %s does not decode to %d bytes", path, ed25519.PublicKeySize)
	}
	if strings.TrimSpace(os.Getenv("BANSOKO_UPDATE_PUBLIC_KEY_PATH")) != "" {
		return nil, errors.New("BANSOKO_UPDATE_PUBLIC_KEY_PATH points at an unreadable file")
	}
	return nil, errors.New("no embedded-update public key found")
}
