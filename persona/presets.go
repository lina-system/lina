// SPDX-License-Identifier: Apache-2.0

// Package persona reads owner-supplied preset material from a local source.
package persona

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// SetupWarning contains no source path or persona text.
const SetupWarning = "Lina persona is unavailable; set persona_source in Core config to a checkout with two nonempty UTF-8 persona files fitting the 64 KiB instruction budget. Continuing with the agent's name."

// Leave room for identity, input and history in the current conversation
// composer's 128,000-byte request budget. This bounds both layers together,
// including the JSON escaping used by the prepared identity block.
const maxLinaBytes = 64 << 10

var ErrUnavailable = errors.New("Lina persona source is unavailable")

// LoadLina reads both fixed layers or returns neither. Public placeholders are
// documentation, never fallback instructions. The source is never modified.
func LoadLina(source string) (core, voice string, err error) {
	if !filepath.IsAbs(source) {
		return "", "", ErrUnavailable
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return "", "", ErrUnavailable
	}
	defer root.Close()
	read := func(name string) (string, error) {
		name = "characters/lina/persona/" + name
		info, err := root.Stat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxLinaBytes {
			return "", ErrUnavailable
		}
		file, err := root.Open(name)
		if err != nil {
			return "", ErrUnavailable
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, maxLinaBytes+1))
		if err != nil || len(data) > maxLinaBytes || !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" {
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
	if len(encodedCore)+len(encodedVoice)-4 > maxLinaBytes {
		return "", "", ErrUnavailable
	}
	return core, voice, nil
}
