// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"errors"
	"os"
	"slices"
)

// The operator owns this opaque file. Core captures its bytes with the records
// generation but never interprets source identities or imports the operator.
const operatorJournal = "system/crw-takeover-journal.json"

func hasOperatorJournal(m Manifest) bool {
	return slices.ContainsFunc(m.Files, func(f File) bool { return f.Path == operatorJournal })
}

func copyOperatorJournal(ctx context.Context, source, dest *os.Root, boundary func(string) error, files *[]File, dirs *[]string) error {
	if info, err := source.Lstat(operatorJournal); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	} else if info.Mode().Perm()&0111 != 0 {
		return ErrInvalid
	}
	if err := regularPath(source, operatorJournal); err != nil {
		return err
	}
	if err := privateDir(dest, "system"); err != nil {
		return err
	}
	*dirs = append(*dirs, "system")
	return copyTree(ctx, source, dest, operatorJournal, boundary, files, dirs)
}
