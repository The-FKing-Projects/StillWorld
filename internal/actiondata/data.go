// Package actiondata validates action payloads and binds their exact bytes to
// registered action names.
package actiondata

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/The-FKing-Projects/StillWorld/action"
)

const (
	MaxInputBytes  = 16 << 10
	MaxResultBytes = 64 << 10
)

const fingerprintDomain = "StillWorld/action-input/v1\x00"

var (
	ErrInvalidInput     = errors.New("action data: invalid input")
	ErrInvalidResult    = errors.New("action data: invalid result")
	ErrInputTooLarge    = errors.New("action data: input too large")
	ErrResultTooLarge   = errors.New("action data: result too large")
	ErrInvalidRejection = errors.New("action data: invalid rejection")
)

// ValidateInput requires one bounded JSON object with no duplicate keys.
func ValidateInput(data []byte) error {
	if len(data) > MaxInputBytes {
		return ErrInputTooLarge
	}
	if err := validateJSON(data, true); err != nil {
		return ErrInvalidInput
	}
	return nil
}

// ValidateResult requires one bounded JSON value with no duplicate keys.
func ValidateResult(data []byte) error {
	if len(data) > MaxResultBytes {
		return ErrResultTooLarge
	}
	if err := validateJSON(data, false); err != nil {
		return ErrInvalidResult
	}
	return nil
}

// ValidateRejection checks the stable code and player-safe message limits.
func ValidateRejection(rejection *action.Rejection) error {
	if rejection == nil || !validRejectionCode(rejection.Code) ||
		len(rejection.Message) == 0 || len(rejection.Message) > 1024 ||
		!utf8.ValidString(rejection.Message) || strings.IndexByte(rejection.Message, 0) >= 0 {
		return ErrInvalidRejection
	}
	return nil
}

// Fingerprint binds the exact input bytes and action name without canonicalizing JSON.
func Fingerprint(actionName string, input []byte) [sha256.Size]byte {
	data := make([]byte, 0, len(fingerprintDomain)+8+len(actionName)+len(input))
	data = append(data, fingerprintDomain...)
	data = binary.BigEndian.AppendUint32(data, uint32(len(actionName)))
	data = append(data, actionName...)
	data = binary.BigEndian.AppendUint32(data, uint32(len(input)))
	data = append(data, input...)
	return sha256.Sum256(data)
}

func validRejectionCode(code string) bool {
	if len(code) == 0 || len(code) > 128 || code[0] == '.' || code[len(code)-1] == '.' {
		return false
	}
	previousDot := false
	namespaced := false
	for i := 0; i < len(code); i++ {
		c := code[i]
		if c == '.' {
			if previousDot {
				return false
			}
			previousDot = true
			namespaced = true
			continue
		}
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
		previousDot = false
	}
	return namespaced
}

func validateJSON(data []byte, requireObject bool) error {
	if len(data) == 0 || !utf8.Valid(data) {
		return ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	if requireObject && first != json.Delim('{') {
		return ErrInvalidInput
	}
	if err := walkValue(decoder, first); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return ErrInvalidInput
	}
	return nil
}

func walkValue(decoder *json.Decoder, first json.Token) error {
	delim, ok := first.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrInvalidInput
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrInvalidInput
			}
			seen[key] = struct{}{}
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := walkValue(decoder, value); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim('}') {
			return ErrInvalidInput
		}
	case '[':
		for decoder.More() {
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := walkValue(decoder, value); err != nil {
				return err
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim(']') {
			return ErrInvalidInput
		}
	default:
		return ErrInvalidInput
	}
	return nil
}
