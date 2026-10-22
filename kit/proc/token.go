// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"bytes"
	"encoding/binary"
)

// KERN_PROCARGS2 contains argc, executable path, padding, argc NUL-terminated
// arguments, then NUL-terminated environment entries. Match only whole entries
// in the environment, never argument text or a token prefix.
func hasProcessToken(data []byte, token string) bool {
	if len(data) < 4 {
		return false
	}
	argc := int(binary.LittleEndian.Uint32(data)) // Darwin amd64 and arm64
	data = data[4:]
	end := bytes.IndexByte(data, 0)
	if end < 0 {
		return false
	}
	data = bytes.TrimLeft(data[end+1:], "\x00")
	for i := 0; i < argc; i++ {
		end = bytes.IndexByte(data, 0)
		if end < 0 {
			return false
		}
		data = data[end+1:]
	}
	for len(data) > 0 {
		end = bytes.IndexByte(data, 0)
		if end <= 0 {
			return false
		}
		if string(data[:end]) == token {
			return true
		}
		data = data[end+1:]
	}
	return false
}
