// SPDX-License-Identifier: Apache-2.0
//go:build !linux && !darwin

package tools

import "os"

func openFileNoFollow(*os.Root, string) (*os.File, error) { return nil, ErrFileScope }
