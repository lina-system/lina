// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
)

func TestTypedResultsNegotiateFromBuildTable(t *testing.T) {
	want := protocol.Capability{Name: "results", Version: "1.0"}
	version, granted, ok := negotiate(manifest.ClientCombinationsFor("linux/amd64"), manifest.TUIPeer, "linux/amd64", []string{"1.0"}, []protocol.Capability{want})
	if !ok || version != "1.0" || len(granted) != 1 || granted[0] != want {
		t.Fatalf("installed result read unavailable: version=%s granted=%v supported=%v", version, granted, ok)
	}
}
