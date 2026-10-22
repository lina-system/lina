// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"testing"

	"github.com/thisisjun786/lina/internal/core/work/pin"
)

func TestAdoptedCRWCorpus(t *testing.T) {
	r, err := pin.Build("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Schemas) != 5 || len(r.Fixtures) != 4 || len(r.Domains) != 4 {
		t.Fatalf("incomplete corpus: %+v", r)
	}
}
