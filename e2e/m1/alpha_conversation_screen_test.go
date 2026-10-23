// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

// Hold the resolved overlay and then a partial conversation redraw. The
// observer must stay blocked until the complete conversation frame arrives.
func TestAlphaConversationScreenWaitsForOverlayClose(t *testing.T) {
	for _, language := range []design.Language{design.Korean, design.English} {
		t.Run(string(language), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				screen, err := tuiterm.NewRecorder(120, 34)
				if err != nil {
					t.Fatal(err)
				}
				defer screen.Close()
				writer := &terminalScreenWriter{screen: screen}
				write := func(frame string) {
					t.Helper()
					if _, err := writer.Write([]byte(frame)); err != nil {
						t.Fatal(err)
					}
				}
				live := design.Text(language, "live")
				shortcuts := "? " + design.Text(language, "shortcuts.title")
				// A question can quote key hints; they alone do not prove closure.
				write("\x1b[?1049h\x1b[2J\x1b[H\x1b[?25lanswer (text): Ctrl+D " + shortcuts +
					"\r\n" + design.Text(language, "resolved") + "\x1b[34;1H" + live + string(terminalWriteEnd))
				h := &harness{t: t, ctx: t.Context()}
				terminal := &terminal{screen: screen}
				result := make(chan tuiterm.Snapshot, 1)
				go func() { result <- alphaConversationScreen(t, h, terminal, language) }()
				assertWaiting := func(stage string) {
					t.Helper()
					synctest.Wait()
					select {
					case <-result:
						t.Fatalf("panel inspection returned before %s finished", stage)
					default:
					}
				}
				assertWaiting("question overlay")
				// A redraw may span PTY reads. Even the composer cursor and footer
				// are insufficient until its right-side panel has also been parsed.
				write("\x1b[2J\x1b[H\x1b[?25h\x1b[34;1H" + live + "  " + shortcuts)
				assertWaiting("partial conversation redraw")
				write(fmt.Sprintf("\x1b[2;81H%s\x1b[33;1H%s", "Fixture work  0m", terminalWriteEnd))
				synctest.Wait()
				select {
				case snapshot := <-result:
					if text := screenText(snapshot); !strings.Contains(text, "Fixture work  0m") || strings.Contains(text, "answer (text):") {
						t.Fatalf("panel inspection did not use the returned conversation frame: %s", text)
					}
				default:
					t.Fatal("complete conversation frame did not release panel inspection")
				}
			})
		})
	}
}
