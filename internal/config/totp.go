package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/The-FKing-Projects/StillWorld/internal/actionname"
	"github.com/The-FKing-Projects/StillWorld/internal/auth"
)

// TotpKeysFromEnv builds a TOTP AEAD key ring from environment variables.
// Returns (nil, nil) when TOTP is not configured. When any TOTP variable is
// set, the active key is required and validation errors are returned.
//
//	STILLWORLD_TOTP_KEY_ID   — active key ID (default "v1")
//	STILLWORLD_TOTP_KEY      — active key: 64 hex chars or base64 of 32 bytes
//	STILLWORLD_TOTP_KEYS     — optional extra decrypt keys as id=material,id=material
func TotpKeysFromEnv() (*auth.TOTPKeyRing, error) {
	return totpKeysFromLookup(os.LookupEnv)
}

func totpKeysFromLookup(lookup func(string) (string, bool)) (*auth.TOTPKeyRing, error) {
	keyID, hasID := lookup("STILLWORLD_TOTP_KEY_ID")
	keyMaterial, hasKey := lookup("STILLWORLD_TOTP_KEY")
	extra, hasExtra := lookup("STILLWORLD_TOTP_KEYS")
	if (!hasID || keyID == "") && (!hasKey || keyMaterial == "") && (!hasExtra || extra == "") {
		return nil, nil
	}
	if keyID == "" {
		keyID = "v1"
	}
	if !actionname.ValidKeyID(keyID) {
		return nil, fmt.Errorf("STILLWORLD_TOTP_KEY_ID is invalid")
	}
	keys := make(map[string][]byte)
	if hasExtra && extra != "" {
		for _, part := range strings.Split(extra, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, material, ok := strings.Cut(part, "=")
			if !ok || id == "" || material == "" {
				return nil, fmt.Errorf("STILLWORLD_TOTP_KEYS entries must be id=material")
			}
			if !actionname.ValidKeyID(id) {
				return nil, fmt.Errorf("STILLWORLD_TOTP_KEYS contains invalid key id %q", id)
			}
			raw, err := parseTOTPKey(material)
			if err != nil {
				return nil, fmt.Errorf("STILLWORLD_TOTP_KEYS %s: %w", id, err)
			}
			keys[id] = raw
		}
	}
	if !hasKey || keyMaterial == "" {
		if _, ok := keys[keyID]; !ok {
			return nil, fmt.Errorf("STILLWORLD_TOTP_KEY is required for active key %q", keyID)
		}
	} else {
		raw, err := parseTOTPKey(keyMaterial)
		if err != nil {
			return nil, fmt.Errorf("STILLWORLD_TOTP_KEY: %w", err)
		}
		keys[keyID] = raw
	}
	return auth.NewTOTPKeyRing(keyID, keys)
}

func parseTOTPKey(material string) ([]byte, error) {
	material = strings.TrimSpace(material)
	if len(material) == 64 {
		raw, err := hex.DecodeString(material)
		if err == nil && len(raw) == 32 {
			return raw, nil
		}
	}
	if raw, err := base64.RawStdEncoding.DecodeString(material); err == nil && len(raw) == 32 {
		return raw, nil
	}
	if raw, err := base64.StdEncoding.DecodeString(material); err == nil && len(raw) == 32 {
		return raw, nil
	}
	if raw, err := base64.RawURLEncoding.DecodeString(material); err == nil && len(raw) == 32 {
		return raw, nil
	}
	if raw, err := base64.URLEncoding.DecodeString(material); err == nil && len(raw) == 32 {
		return raw, nil
	}
	return nil, fmt.Errorf("must be 32 bytes as 64 hex characters or base64")
}
