package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestAccountStatusCommandsRequireUsername(t *testing.T) {
	for _, command := range []string{"disable-account", "enable-account"} {
		var out, errOut bytes.Buffer
		if code := run([]string{command}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), command) {
			t.Errorf("%s: code=%d error=%q", command, code, errOut.String())
		}
	}
}
