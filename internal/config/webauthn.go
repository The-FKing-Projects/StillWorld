package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
)

// WebAuthnFromEnv builds optional WebAuthn RP settings from environment
// variables. Returns (nil, nil) when passkeys are not configured. When any
// WebAuthn variable is set, RP ID and at least one origin are required.
//
//	STILLWORLD_WEBAUTHN_RP_ID           — effective domain (e.g. staging.example.com)
//	STILLWORLD_WEBAUTHN_RP_ORIGINS      — comma-separated origins (https://staging.example.com)
//	STILLWORLD_WEBAUTHN_RP_DISPLAY_NAME — optional display name (default StillWorld)
func WebAuthnFromEnv() (*auth.WebAuthnConfig, error) {
	return webAuthnFromLookup(os.LookupEnv)
}

func webAuthnFromLookup(lookup func(string) (string, bool)) (*auth.WebAuthnConfig, error) {
	rpID, hasID := lookup("STILLWORLD_WEBAUTHN_RP_ID")
	origins, hasOrigins := lookup("STILLWORLD_WEBAUTHN_RP_ORIGINS")
	display, hasDisplay := lookup("STILLWORLD_WEBAUTHN_RP_DISPLAY_NAME")
	if (!hasID || rpID == "") && (!hasOrigins || origins == "") && (!hasDisplay || display == "") {
		return nil, nil
	}
	rpID = strings.TrimSpace(rpID)
	if rpID == "" {
		return nil, fmt.Errorf("STILLWORLD_WEBAUTHN_RP_ID is required when WebAuthn is configured")
	}
	var originList []string
	for _, part := range strings.Split(origins, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			originList = append(originList, part)
		}
	}
	if len(originList) == 0 {
		return nil, fmt.Errorf("STILLWORLD_WEBAUTHN_RP_ORIGINS must list at least one origin")
	}
	display = strings.TrimSpace(display)
	if display == "" {
		display = "StillWorld"
	}
	return &auth.WebAuthnConfig{
		RPID:          rpID,
		RPOrigins:     originList,
		RPDisplayName: display,
	}, nil
}
