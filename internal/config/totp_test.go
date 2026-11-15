package config

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestTotpKeysFromLookup(t *testing.T) {
	key := bytes.Repeat([]byte{0x5a}, 32)
	hexKey := hex.EncodeToString(key)
	ring, err := totpKeysFromLookup(lookupFrom(map[string]string{
		"STILLWORLD_TOTP_KEY_ID": "v1",
		"STILLWORLD_TOTP_KEY":    hexKey,
	}))
	if err != nil || ring == nil {
		t.Fatalf("got %#v %v", ring, err)
	}

	ring, err = totpKeysFromLookup(lookupFrom(map[string]string{}))
	if err != nil || ring != nil {
		t.Fatalf("empty env: %#v %v", ring, err)
	}

	_, err = totpKeysFromLookup(lookupFrom(map[string]string{
		"STILLWORLD_TOTP_KEY_ID": "v1",
	}))
	if err == nil {
		t.Fatal("expected missing key error")
	}

	old := hex.EncodeToString(bytes.Repeat([]byte{0x01}, 32))
	ring, err = totpKeysFromLookup(lookupFrom(map[string]string{
		"STILLWORLD_TOTP_KEY_ID": "v2",
		"STILLWORLD_TOTP_KEY":    hexKey,
		"STILLWORLD_TOTP_KEYS":   "v1=" + old,
	}))
	if err != nil || ring == nil {
		t.Fatalf("rotation keys: %#v %v", ring, err)
	}
}
