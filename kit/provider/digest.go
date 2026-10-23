// SPDX-License-Identifier: Apache-2.0
package provider

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

// RequestDigest covers exact logical request bytes and optional Core metadata.
// Credentials and the live runtime tool bridge are not model input.
func RequestDigest(r Request) string {
	h := sha256.New()
	write := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	write([]byte("lina-request-v1"))
	meta, _ := json.Marshal([]string{string(r.Model), string(r.Effort), r.Instructions, string(r.Purpose)})
	write(meta)
	fields, _ := json.Marshal(r.Fields)
	write(fields)
	for _, list := range [][]Item{r.Tools, r.Items} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(list)))
		h.Write(n[:])
		for _, raw := range list {
			write(raw)
		}
	}
	if metadata := RequestMetadata(r); metadata != nil {
		write(metadata)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RunCacheIdentity hashes only the durable run id under its own domain.
func RunCacheIdentity(run string) string {
	b, _ := json.Marshal([]string{"lina-cache-run-v1", run})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
