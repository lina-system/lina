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

func TestLoadPairRequiresBothLocalTextLayers(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "empty", "directory", "oversize", "invalid UTF-8", "escape"} {
		t.Run(mode, func(t *testing.T) {
			source := t.TempDir()
			dir := filepath.Join(source, "lina")
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
			core, gotVoice, err := loadLocal(t, source)
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
		if core, voice, err := loadLocal(t, source); !errors.Is(err, persona.ErrUnavailable) || core != "" || voice != "" {
			t.Fatal("invalid source accepted")
		}
	}
}

func TestLoadPairReservesConversationBudget(t *testing.T) {
	for _, sizes := range [][2]int{{129000, 1}, {40000, 40000}, {65535, 1}} {
		source := t.TempDir()
		dir := filepath.Join(source, "lina")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for i, name := range []string{"core-v1.md", "default-voice-v1.md"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("x", sizes[i])), 0600); err != nil {
				t.Fatal(err)
			}
		}
		core, voice, err := loadLocal(t, source)
		if sizes[0]+sizes[1] > 65536 {
			if !errors.Is(err, persona.ErrUnavailable) || core != "" || voice != "" {
				t.Errorf("oversized preset accepted: sizes=%v, loaded=%d+%d, err=%v", sizes, len(core), len(voice), err)
			}
		} else if err != nil || len(core) != sizes[0] || len(voice) != sizes[1] {
			t.Errorf("bounded preset rejected: sizes=%v, err=%v", sizes, err)
		}
	}
}

func TestLoadPairBoundsEncodedLayers(t *testing.T) {
	source := t.TempDir()
	dir := filepath.Join(source, "lina")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"core-v1.md": strings.Repeat("<", 25000), "default-voice-v1.md": "fixture voice"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// The prepared identity JSON escapes each '<' to six bytes. Its raw file
	// size must not hide an instruction block larger than the request budget.
	if core, voice, err := loadLocal(t, source); !errors.Is(err, persona.ErrUnavailable) || core != "" || voice != "" {
		t.Fatalf("escaped oversized preset accepted: loaded=%d+%d, err=%v", len(core), len(voice), err)
	}
}

func loadLocal(t *testing.T, source string) (string, string, error) {
	t.Helper()
	root, err := os.OpenRoot(source)
	if err != nil {
		return "", "", persona.ErrUnavailable
	}
	defer root.Close()
	return persona.Load(root.FS(), "lina")
}
