// SPDX-License-Identifier: Apache-2.0
package waiting_test

import (
	"github.com/thisisjun786/lina/internal/core/waiting"
	"testing"
	"time"
)

func TestRoundSourceRereadsCompletion(t *testing.T) {
	owner, db, _ := rounds(t)
	r := round()
	if err := owner.Open(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	service := waiting.New(db, map[string]waiting.Source{"question": waiting.RoundSource{}})
	list, err := service.Build(t.Context(), r.Person, waiting.Options{Now: time.Now()})
	if err != nil || len(list.Entries) != len(r.Questions) {
		t.Fatalf("pending: %+v %v", list, err)
	}
	if err = owner.Expire(t.Context(), r.Deadline); err != nil {
		t.Fatal(err)
	}
	list, err = service.Build(t.Context(), r.Person, waiting.Options{Now: r.Deadline})
	if err != nil || len(list.Entries) != 0 {
		t.Fatalf("finished source still shown: %+v %v", list, err)
	}
}
