// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestResultPagesReadPinnedMaterialAndEnforceOwner(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("가나다🙂", 4000) + "END-OF-RESULT"
	s, _, store := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		return answer(runs.Artifact{Body: body, Done: true}), nil
	})
	id := start(t, s)
	if _, _, err := s.ResultPage(t.Context(), origin().Person, id, 0); !errors.Is(err, runs.ErrState) {
		t.Fatal("opened unlanded run", err)
	}
	release(t, s, id)
	r := wait(t, s, id)
	// A newer material revision must not silently replace the landed reference.
	changed, _ := json.Marshal(struct{ Artifact runs.Artifact }{runs.Artifact{Body: "replacement"}})
	if _, err := store.SaveRunResult(t.Context(), id, changed, assets.Actor{PersonID: origin().Person, AgentID: origin().Agent}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResultPage(t.Context(), "another-person", id, 0); !errors.Is(err, runs.ErrScope) {
		t.Fatal("cross-owner material read", err)
	}
	for _, offset := range []int64{-1, 1, int64(len(body)) + 1} {
		if _, _, err := s.ResultPage(t.Context(), origin().Person, id, offset); !errors.Is(err, runs.ErrInvalid) {
			t.Fatalf("bad offset %d: %v", offset, err)
		}
	}
	var all strings.Builder
	offset := int64(0)
	for {
		page, next, err := s.ResultPage(t.Context(), r.Origin.Person, id, offset)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 16<<10 || !utf8.ValidString(page) {
			t.Fatal("unbounded or split UTF-8 page")
		}
		all.WriteString(page)
		if next == 0 {
			break
		}
		if next <= offset {
			t.Fatal("nonadvancing page")
		}
		offset = next
	}
	if all.String() != body {
		t.Fatal("material body truncated or replaced")
	}
}
