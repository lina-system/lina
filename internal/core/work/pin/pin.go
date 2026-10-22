// SPDX-License-Identifier: Apache-2.0

// Package pin verifies and executes the CRW inputs to LINA's work ledger.
package pin

import (
	"io/fs"
	"regexp"
	"strings"

	"github.com/thisisjun786/lina/internal/core/manifest/crwpin"
)

const Repository = crwpin.Repository

type Record = crwpin.Record
type File = crwpin.File
type ReplayFile = crwpin.ReplayFile

var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validPath(path string) bool {
	return fs.ValidPath(path) && path != "." && !strings.ContainsAny(path, "\\:\x00")
}
