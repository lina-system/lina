// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"slices"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Combination is a tested row from the build's supported-combination table.
// Product versions deliberately do not participate in compatibility.
type Combination = manifest.Combination

func negotiate(rows []Combination, peer, platform string, versions []string, offered []protocol.Capability) (string, []protocol.Capability, bool) {
	var selected *Combination
	for i := range rows {
		row := &rows[i]
		if row.Connection != "client" || row.Peer != peer || row.Platform != platform || len(row.Evidence) == 0 || !slices.Contains(versions, row.ProtocolVersion) || !versionPattern.MatchString(row.ProtocolVersion) {
			continue
		}
		if selected == nil || higherVersion(row.ProtocolVersion, selected.ProtocolVersion) {
			selected = row
		}
	}
	if selected == nil {
		return "", nil, false
	}
	capabilities := []protocol.Capability{}
	for _, capability := range selected.Capabilities {
		if slices.Contains(offered, capability) && !slices.Contains(capabilities, capability) {
			capabilities = append(capabilities, capability)
		}
	}
	return selected.ProtocolVersion, capabilities, true
}

func higherVersion(a, b string) bool {
	// Compare decimal components without machine integer limits.
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := range left {
		if len(left[i]) != len(right[i]) {
			return len(left[i]) > len(right[i])
		}
		if left[i] != right[i] {
			return left[i] > right[i]
		}
	}
	return false
}

// EpochNumber enforces CP-02 at the service boundary, without a float conversion.
func EpochNumber(epoch string) (uint64, error) {
	n, err := strconv.ParseUint(epoch, 10, 64)
	if err != nil {
		return 0, err
	}
	if strconv.FormatUint(n, 10) != epoch {
		return 0, strconv.ErrSyntax
	}
	return n, nil
}

// LocalCombinations is the compiled table also emitted by the build writer.
func LocalCombinations() []Combination { return manifest.ClientCombinations() }
