// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"testing"
	"time"
)

func TestMainHostCapacityUsesMeasuredLoadMemoryAndDisk(t *testing.T) {
	n, err := hostSample("main", "8.0 4.0 2.0 1/100 99\n", "MemTotal: 16000000 kB\nMemAvailable: 8000000 kB\n", 16000, 8, Resources{CPU: 6, MemoryMB: 10000, DiskMB: 12000}, map[string]bool{"worker": true}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != "main" || n.LoadCPU != 4 || n.AvailableMemoryMB != 7812 || Slots(n, nil, Resources{CPU: 2, MemoryMB: 1000, DiskMB: 2000}, time.Unix(100, 0)) != 2 {
		t.Fatalf("main host slot report: %+v", n)
	}
	for _, load := range []string{"bad", "1 NaN 1", "1 -1 1"} {
		if _, err = hostSample("main", load, "MemAvailable: 8000000 kB\n", 16000, 8, Resources{CPU: 6, MemoryMB: 10000, DiskMB: 12000}, nil, time.Unix(100, 0)); err == nil {
			t.Fatalf("accepted invalid load %q", load)
		}
	}
}
