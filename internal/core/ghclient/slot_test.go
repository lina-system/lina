// SPDX-License-Identifier: Apache-2.0
package ghclient

import (
	"context"
	"testing"
	"testing/synctest"
)

func TestMergeSlotsSerializeOnlySharedBaseAndCancelWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testClient(t, &fakeRunner{})
		release, err := c.takeSlot(t.Context(), "o/r/dev")
		if err != nil {
			t.Fatal(err)
		}
		acquired := make(chan func(), 1)
		go func() {
			unlock, e := c.takeSlot(t.Context(), "o/r/dev")
			if e != nil {
				t.Error(e)
				return
			}
			acquired <- unlock
		}()
		synctest.Wait()
		select {
		case <-acquired:
			t.Fatal("shared base admitted concurrently")
		default:
		}
		other, err := c.takeSlot(t.Context(), "o/r/other")
		if err != nil {
			t.Fatal(err)
		}
		other()
		ctx, cancel := context.WithCancel(t.Context())
		cancelled := make(chan error, 1)
		go func() { _, e := c.takeSlot(ctx, "o/r/dev"); cancelled <- e }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if err = <-cancelled; err != context.Canceled {
			t.Fatal(err)
		}
		release()
		synctest.Wait()
		(<-acquired)()
		if len(c.slots) != 0 {
			t.Fatal("slot leaked")
		}
	})
}
