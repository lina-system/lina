// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"testing"
)

func TestAdoptedCRWCorpus(t *testing.T) {
	r, err := CRWBuild("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Schemas) != 5 || len(r.Fixtures) != 4 || len(r.Domains) != 4 {
		t.Fatalf("incomplete corpus: %+v", r)
	}
}
