// SPDX-License-Identifier: Apache-2.0
package work

import (
	"errors"
	"reflect"
	"testing"
)

func TestDirectoryAreaIntersections(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"src", "src/a.go", true},
		{"src", "src/deep/a.go", true},
		{"src/*", "src/pkg/deep/a.go", true},
		{"src/p?g", "src/pkg/a.go", true},
		{"src/[pq]kg", "src/pkg/a.go", true},
		{"**/pkg", "src/pkg/a.go", true},
		{"src", "src2/a.go", false},
		{"src/pkg", "src/pkg2/a.go", false},
		{"src/[pq]kg", "src/rkg/a.go", false},
	} {
		t.Run(tc.a+"_"+tc.b, func(t *testing.T) {
			for _, pair := range [][2]string{{tc.a, tc.b}, {tc.b, tc.a}} {
				got, err := ScheduledAreasOverlap([]string{pair[0]}, []string{pair[1]})
				if err != nil || got != tc.want {
					t.Errorf("%q / %q = %v, %v; want %v", pair[0], pair[1], got, err, tc.want)
				}
			}
		})
	}
}

func TestDirectoryAreaAdmissionAndLease(t *testing.T) {
	t.Parallel()
	for _, area := range []string{"src", "src/*"} {
		t.Run(area, func(t *testing.T) {
			s, _ := testStore(t)
			a, b, c := scheduled("a"), scheduled("b"), scheduled("c")
			a.Schedule.Areas = []string{area}
			b.Schedule.Areas = []string{"src/pkg/a.go"}
			c.Schedule.Areas = []string{"src2/a.go"}
			if err := s.Schedule(t.Context(), []Task{a, b, c}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("admitted overlapping directory and child: %v", err)
			}
			if _, err := s.Get(t.Context(), "a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("partial graph admission: %v", err)
			}
			b.Schedule.Group = "other"
			if err := s.Schedule(t.Context(), []Task{a, b, c}); err != nil {
				t.Fatal(err)
			}
			released, err := s.Release(t.Context(), available())
			if err != nil || !reflect.DeepEqual(ids(released), []string{"a", "c"}) {
				t.Fatalf("directory lease did not exclude child: %v, %v", ids(released), err)
			}
			card, err := s.Get(t.Context(), "b")
			if err != nil || card.WaitReason == nil || card.WaitReason.Kind != "area" || card.WaitReason.Target != "a" {
				t.Fatalf("child has no directory wait: %+v, %v", card.WaitReason, err)
			}
		})
	}
}
