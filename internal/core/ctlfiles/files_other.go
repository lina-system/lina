// SPDX-License-Identifier: Apache-2.0
//go:build !linux

package ctlfiles

import (
	"errors"
	"os"
)

const noFollow = 0
const nonBlock = 0

func owned(os.FileInfo, bool) bool { return false }
func unlinked(os.FileInfo) bool    { return false }
func checkLocal(*os.File) error    { return errors.New("Core requires a Linux state root") }
func localFilesystem(int64) bool   { return false }
func lockFile(*os.File) error      { return errors.New("Core requires Linux flock") }
