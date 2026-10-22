// SPDX-License-Identifier: Apache-2.0

// Package persona provides the shipped working personas. World material is not
// a persona source and enters conversations only through material recall.
package persona

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"unicode/utf8"
)

// Embed the directories so a missing pair can fail per preset at runtime, while
// the release validator rejects incomplete source trees before building.
//
//go:embed system lina dev knowledge life creative investing legal
var shipped embed.FS

// Shipped returns the immutable working layers included in this binary.
func Shipped() fs.FS { return shipped }

type Starter struct{ Key, Name string }

// Starters returns a fresh catalog so callers cannot change the shipped keys.
func Starters() []Starter {
	return []Starter{{"system", "Selene"}, {"lina", "Lina"}, {"dev", "Sion"}, {"knowledge", "Rumi"}, {"life", "Arin"}, {"creative", "Cleo"}, {"investing", "Kai"}, {"legal", "Kanna"}}
}

func IsStarter(key string) bool {
	for _, s := range Starters() {
		if s.Key == key {
			return true
		}
	}
	return false
}

// Leave room for identity, tools, input and history in the conversation budget.
const maxPairBytes = 64 << 10

var ErrUnavailable = errors.New("shipped persona pair unavailable")

// SetupWarning accepts only catalog keys and includes no paths or file content.
func SetupWarning(key string) string {
	if !IsStarter(key) {
		key = "unknown"
	}
	return fmt.Sprintf("Shipped persona %s: pair unavailable or invalid; continuing with identity only.", key)
}

// Load reads both layers or neither. The source must confine access (embed.FS or
// os.Root.FS); a private configuration path must never be supplied by Core.
func Load(source fs.FS, key string) (core, voice string, err error) {
	if source == nil || !IsStarter(key) {
		return "", "", ErrUnavailable
	}
	read := func(name string) (string, error) {
		info, err := fs.Stat(source, key+"/"+name)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxPairBytes {
			return "", ErrUnavailable
		}
		file, err := source.Open(key + "/" + name)
		if err != nil {
			return "", ErrUnavailable
		}
		defer file.Close()
		info, err = file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxPairBytes {
			return "", ErrUnavailable
		}
		data, err := io.ReadAll(io.LimitReader(file, maxPairBytes+1))
		if err != nil || len(data) > maxPairBytes || !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" {
			return "", ErrUnavailable
		}
		return string(data), nil
	}
	core, err = read("core-v1.md")
	if err != nil {
		return "", "", err
	}
	voice, err = read("default-voice-v1.md")
	if err != nil {
		return "", "", err
	}
	encodedCore, _ := json.Marshal(core)
	encodedVoice, _ := json.Marshal(voice)
	if len(encodedCore)+len(encodedVoice)-4 > maxPairBytes {
		return "", "", ErrUnavailable
	}
	return core, voice, nil
}

// Validate checks each pair independently, without logging source data.
func Validate(source fs.FS) error {
	for _, s := range Starters() {
		if _, _, err := Load(source, s.Key); err != nil {
			return fmt.Errorf("%s: %w", s.Key, err)
		}
	}
	return nil
}
