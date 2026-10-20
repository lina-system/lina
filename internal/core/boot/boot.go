// SPDX-License-Identifier: Apache-2.0

// Package boot owns the empty Core's start and shutdown sequence.
package boot

import (
	"context"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
)

// Run serves the client socket until cancellation. This skeleton has no state
// root, control files, persistent identity, epoch counter or database.
func Run(ctx context.Context, socket string) error {
	listener, err := clientsrv.Listen(socket)
	if err != nil {
		return err
	}
	return clientsrv.Serve(ctx, listener)
}
