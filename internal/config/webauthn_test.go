package config

import "testing"

func TestWebAuthnFromEnvOptional(t *testing.T) {
	cfg, err := webAuthnFromLookup(func(string) (string, bool) { return "", false })
	if err != nil || cfg != nil {
		t.Fatalf("got %#v %v", cfg, err)
	}
}

func TestWebAuthnFromEnvEmptyPresentIsConfigured(t *testing.T) {
	_, err := webAuthnFromLookup(func(k string) (string, bool) {
		if k == "STILLWORLD_WEBAUTHN_RP_ID" {
			return "", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("expected missing RP ID error for empty present var")
	}
	_, err = webAuthnFromLookup(func(k string) (string, bool) {
		switch k {
		case "STILLWORLD_WEBAUTHN_RP_ID":
			return "localhost", true
		case "STILLWORLD_WEBAUTHN_RP_ORIGINS":
			return "  ", true
		default:
			return "", false
		}
	})
	if err == nil {
		t.Fatal("expected missing origins error for blank present var")
	}
}

func TestWebAuthnFromEnvRequiresIDAndOrigins(t *testing.T) {
	_, err := webAuthnFromLookup(func(k string) (string, bool) {
		if k == "STILLWORLD_WEBAUTHN_RP_ID" {
			return "example.com", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("expected origins error")
	}
	cfg, err := webAuthnFromLookup(func(k string) (string, bool) {
		switch k {
		case "STILLWORLD_WEBAUTHN_RP_ID":
			return "localhost", true
		case "STILLWORLD_WEBAUTHN_RP_ORIGINS":
			return "http://localhost:8080, https://staging.example.com", true
		case "STILLWORLD_WEBAUTHN_RP_DISPLAY_NAME":
			return "Staging", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RPID != "localhost" || cfg.RPDisplayName != "Staging" || len(cfg.RPOrigins) != 2 {
		t.Fatalf("%#v", cfg)
	}
}
