// SPDX-License-Identifier: Apache-2.0
package channels

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

const PairingLifetime = 5 * time.Minute

// PairingCode returns a cryptographically random, single-use code. Only its
// hash is kept in canon. Issuing another code invalidates the preceding one.
func (s *Store) PairingCode(ctx context.Context, b Bot, person string, now time.Time) (string, error) {
	if !b.Valid() || !present(person) || now.UnixNano() <= 0 {
		return "", ErrInvalid
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	code := hex.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(code))
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := owner(ctx, tx, b, person); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE channel_connections SET pairing_hash=?,pairing_expires=? WHERE bot=?", hash[:], now.Add(PairingLifetime).UnixNano(), b.Key())
		return err
	})
	if err != nil {
		return "", err
	}
	return code, nil
}
func numeric(s string) bool {
	if s == "" || s == "0" || s[0] == '0' {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func (s *Store) Pair(ctx context.Context, b Bot, code, sender, privateChat string, now time.Time) error {
	if !b.Valid() || !numeric(sender) || !present(privateChat) || now.UnixNano() <= 0 {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		var stored []byte
		var expires int64
		var paired string
		if err := tx.QueryRowContext(ctx, "SELECT pairing_hash,pairing_expires,sender FROM channel_connections WHERE bot=?", b.Key()).Scan(&stored, &expires, &paired); err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(code))
		if now.UnixNano() >= expires || subtle.ConstantTimeCompare(stored, hash[:]) != 1 {
			return ErrDenied
		}
		// A replacement code may refresh the same identity, never silently transfer it.
		if paired != "" && paired != sender {
			return ErrDenied
		}
		_, err := tx.ExecContext(ctx, "UPDATE channel_connections SET sender=?,private_chat=?,pairing_hash=NULL,pairing_expires=0 WHERE bot=?", sender, privateChat, b.Key())
		return err
	})
}
