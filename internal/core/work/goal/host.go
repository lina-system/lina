// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"strconv"
	"strings"
	"time"
)

func hostSample(id, load, memory string, disk int64, cores int, limits Resources, workers map[string]bool, at time.Time) (Node, error) {
	fields := strings.Fields(load)
	if id == "" || len(fields) < 3 || cores <= 0 || !validResources(limits) || at.Unix() <= 0 || disk < 0 {
		return Node{}, ErrInvalid
	}
	load5, err := strconv.ParseFloat(fields[1], 64)
	if err != nil || !finite(load5) || load5 < 0 {
		return Node{}, ErrInvalid
	}
	available := int64(-1)
	for _, line := range strings.Split(memory, "\n") {
		parts := strings.Fields(line)
		if len(parts) != 3 || parts[0] != "MemAvailable:" {
			continue
		}
		if available != -1 || parts[2] != "kB" {
			return Node{}, ErrInvalid
		}
		value, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || value < 0 {
			return Node{}, ErrInvalid
		}
		available = value / 1024
	}
	if available < 0 {
		return Node{}, ErrInvalid
	}
	ready := map[string]bool{}
	for name, ok := range workers {
		ready[name] = ok
	}
	return Node{ID: id, Connected: true, ObservedAt: at.Unix(), CPUCores: float64(cores), CPULimit: limits.CPU, LoadCPU: load5, AvailableMemoryMB: available, MemoryLimitMB: limits.MemoryMB, AvailableDiskMB: disk, DiskLimitMB: limits.DiskMB, Workers: ready}, nil
}
