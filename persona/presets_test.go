// SPDX-License-Identifier: Apache-2.0

package persona_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/persona"
)

func TestLoadLinaRequiresBothLocalTextLayers(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "empty", "directory", "oversize", "invalid UTF-8", "escape"} {
		t.Run(mode, func(t *testing.T) {
			source := t.TempDir()
			dir := filepath.Join(source, "characters/lina/persona")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "core-v1.md"), []byte("fixture core\n"), 0600); err != nil {
				t.Fatal(err)
			}
			voicePath := filepath.Join(dir, "default-voice-v1.md")
			voice := "fixture voice\n"
			switch mode {
			case "empty":
				voice = "\n  "
			case "oversize":
				voice = strings.Repeat("x", (1<<20)+1)
			case "invalid UTF-8":
				voice = string([]byte{0xff})
			}
			switch mode {
			case "missing":
			case "directory":
				if err := os.Mkdir(voicePath, 0700); err != nil {
					t.Fatal(err)
				}
			case "escape":
				outside := filepath.Join(t.TempDir(), "outside.md")
				if err := os.WriteFile(outside, []byte(voice), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, voicePath); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(voicePath, []byte(voice), 0600); err != nil {
					t.Fatal(err)
				}
			}
			core, gotVoice, err := persona.LoadLina(source)
			if mode == "valid" {
				if err != nil || core != "fixture core\n" || gotVoice != voice {
					t.Fatalf("fixture not preserved: %q %q %v", core, gotVoice, err)
				}
			} else if !errors.Is(err, persona.ErrUnavailable) || core != "" || gotVoice != "" {
				t.Fatalf("partial or invalid preset accepted: %q %q %v", core, gotVoice, err)
			}
		})
	}
	for _, source := range []string{"", "relative", filepath.Join(t.TempDir(), "missing")} {
		if core, voice, err := persona.LoadLina(source); !errors.Is(err, persona.ErrUnavailable) || core != "" || voice != "" {
			t.Fatal("invalid source accepted")
		}
	}
}
