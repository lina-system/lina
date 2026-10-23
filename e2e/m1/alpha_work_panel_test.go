// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"strings"
	"testing"
	"testing/synctest"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

// A connected conversation can precede the resumed worker's progress update.
// Hold that frame until the observer has blocked, then publish the whole card.
func TestAlphaWorkCardWaitsForProgress(t *testing.T) {
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
					if _, err := writer.Write([]byte(frame + string(terminalWriteEnd))); err != nil {
						t.Fatal(err)
					}
				}
				write("\x1b[?1049h\x1b[2J\x1b[H\x1b[?25h\x1b[34;1H" +
					design.Text(language, "live") + "  ? " + design.Text(language, "shortcuts.title"))
				h := &harness{t: t, ctx: t.Context()}
				terminal := &terminal{screen: screen}
				result := make(chan tuiterm.Snapshot, 1)
				go func() { result <- alphaWorkCardScreen(t, h, terminal, language) }()
				assertWaiting := func(stage string) {
					t.Helper()
					synctest.Wait()
					select {
					case <-result:
						t.Fatalf("card inspection returned before %s", stage)
					default:
					}
				}
				assertWaiting("worker progress arrived")
				// The title alone does not prove the required activity is visible.
				write("\x1b[3;81HFixture work  1m")
				assertWaiting("worker activity arrived")
				write("\x1b[4;81H  Applying answer\x1b[33;1H")
				synctest.Wait()
				select {
				case snapshot := <-result:
					if panel := strings.Join(panelCellRows(snapshot, 80, 40), "\n"); !alphaHasWorkCard(panel) {
						t.Fatalf("observer returned a frame without the complete card: %s", panel)
					}
				default:
					t.Fatal("complete worker card did not release inspection")
				}
			})
		})
	}
}

func TestAlphaWorkCardElapsed(t *testing.T) {
	for _, elapsed := range []string{"0m", "1m", "59m", "1h 00m", "1h 05m", "23h 59m", "1d 00h", "2d 04h", "100d 23h"} {
		t.Run(elapsed, func(t *testing.T) {
			panel := "일\n테스트 작업    " + elapsed + "\n  답변 반영\n"
			if !alphaHasWorkCard(panel) {
				t.Fatalf("valid work card rejected at elapsed %s", elapsed)
			}
		})
	}
	for name, panel := range map[string]string{
		"empty":             "일\n",
		"no name":           "0m\n  답변 반영\n",
		"no activity":       "테스트 작업 0m\n  \n",
		"elapsed in prose":  "테스트 작업\n  0m mentioned in activity\n",
		"unknown elapsed":   "테스트 작업 ?\n  답변 반영\n",
		"invalid minutes":   "테스트 작업 60m\n  답변 반영\n",
		"invalid remainder": "테스트 작업 1h 60m\n  답변 반영\n",
		"invalid hours":     "테스트 작업 1d 24h\n  답변 반영\n",
	} {
		t.Run(name, func(t *testing.T) {
			if alphaHasWorkCard(panel) {
				t.Fatal("accepted panel without a named work card, elapsed time and activity")
			}
		})
	}
}
