// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"fmt"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
)

// addOperations validates the entire batch before changing the installed map.
// Deliberate decoration of an existing operation belongs in wrapOperation.
func addOperations(identity *clientsrv.Identity, groups ...map[protocol.Kind]clientsrv.Operation) error {
	seen := make(map[protocol.Kind]bool)
	for _, group := range groups {
		for kind := range group {
			if _, exists := identity.Operations[kind]; exists || seen[kind] {
				return fmt.Errorf("duplicate client operation %q", kind)
			}
			seen[kind] = true
		}
	}
	if identity.Operations == nil {
		identity.Operations = make(map[protocol.Kind]clientsrv.Operation)
	}
	for _, group := range groups {
		for kind, operation := range group {
			identity.Operations[kind] = operation
		}
	}
	return nil
}

func (r *conversationRuntime) wrapOperation(kind protocol.Kind, wrap func(clientsrv.Operation) clientsrv.Operation) error {
	operation, exists := r.identity.Operations[kind]
	if !exists {
		return fmt.Errorf("cannot wrap missing client operation %q", kind)
	}
	r.identity.Operations[kind] = wrap(operation)
	return nil
}

// chainRefresh runs hooks in registration order, stopping at the first error.
func (r *conversationRuntime) chainRefresh(next func(context.Context, string) error) {
	previous := r.identity.RefreshTopic
	r.identity.RefreshTopic = func(ctx context.Context, topic string) error {
		if previous != nil {
			if err := previous(ctx, topic); err != nil {
				return err
			}
		}
		return next(ctx, topic)
	}
}
