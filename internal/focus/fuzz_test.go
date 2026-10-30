// SPDX-License-Identifier: MIT

package focus

import (
	"bytes"
	"fmt"
	"testing"
	"testing/synctest"
)

// readReply sends a request over a port that returns stream at most size
// bytes a Read, or all of it at once when size is 0, and returns the reply.
func readReply(t *testing.T, stream []byte, size int) (string, error) {
	c := &client{rw: &scripted{reply: bytes.Clone(stream), size: size}}
	return c.request(t.Context(), "test", nil)
}

// FuzzClientRead checks that how a reply is split into reads changes
// nothing: the same reply comes out, or the same error stops it.
func FuzzClientRead(f *testing.F) {
	f.Add([]byte("true \r\n.\r\n"), uint8(1))
	f.Add([]byte("help\r\nversion\r\n.\r\nupgrade.start\r\n"), uint8(5))
	f.Add([]byte("true\x00\r\n.\r\n"), uint8(3))
	f.Add([]byte("true\r\n"), uint8(2))
	f.Fuzz(func(t *testing.T, stream []byte, size uint8) {
		synctest.Test(t, func(t *testing.T) {
			whole, wholeErr := readReply(t, stream, 0)
			split, splitErr := readReply(t, stream, int(size%32)+1)
			if whole != split || fmt.Sprint(wholeErr) != fmt.Sprint(splitErr) {
				t.Errorf("read whole: %q, %v; split: %q, %v", whole, wholeErr, split, splitErr)
			}
		})
	})
}
