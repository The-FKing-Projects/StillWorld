// Package actionname validates identifiers used by the action protocol.
package actionname

const (
	MaxGameIDLength = 32
	MaxActionLength = 128
	MaxKeyIDLength  = 32
)

// ValidGameID reports whether id matches the registered game ID grammar.
func ValidGameID(id string) bool {
	if len(id) == 0 || len(id) > MaxGameIDLength || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for i := 1; i < len(id); i++ {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// ValidActionName reports whether name is a nonempty, namespaced action name.
func ValidActionName(name string) bool {
	if len(name) == 0 || len(name) > MaxActionLength || name[0] == '.' || name[len(name)-1] == '.' {
		return false
	}
	previousDot := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '.' {
			if previousDot {
				return false
			}
			previousDot = true
			continue
		}
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
		previousDot = false
	}
	return true
}

// ValidKeyID reports whether id is safe as a token key identifier segment.
func ValidKeyID(id string) bool {
	if len(id) == 0 || len(id) > MaxKeyIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') &&
			(c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
