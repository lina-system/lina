// SPDX-License-Identifier: Apache-2.0

package term

import (
	"os"

	"github.com/charmbracelet/x/term"
)

// IsTerminal reports whether the file is a terminal.
func IsTerminal(file *os.File) bool { return term.IsTerminal(file.Fd()) }

// ReadSecret reads one line without echoing it, so a value the person types,
// such as a password, never shows on the terminal. It requires a terminal.
func ReadSecret(file *os.File) ([]byte, error) { return term.ReadPassword(file.Fd()) }
