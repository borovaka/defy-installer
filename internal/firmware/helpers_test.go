// SPDX-License-Identifier: MIT

package firmware

import (
	"crypto/sha256"
	"strings"
	"testing"
)

// pin points one of the pinned SHA-256 at data for the rest of the test.
func pin(t *testing.T, pinned *[32]byte, data []byte) {
	t.Helper()
	old := *pinned
	*pinned = sha256.Sum256(data)
	t.Cleanup(func() { *pinned = old })
}

func wantError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Errorf("no error, want one containing %q", contains)
	} else if !strings.Contains(err.Error(), contains) {
		t.Errorf("error %q does not contain %q", err, contains)
	}
}
