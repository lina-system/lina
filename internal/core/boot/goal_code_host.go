// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"runtime"
	"time"

	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/proc"
)

// An optional installed monitor supplies measurements for environments where
// Core cannot read the host's counters directly. It supplies no authority,
// worker readiness, identity or capacity ceilings.
func (r *conversationRuntime) codeHost(ctx context.Context, cfg codeConnections, workers map[string]bool) (goal.Node, error) {
	if cfg.HostReport == nil {
		return goal.MainHost(ctx, r.main, r.root, cfg.Limits, workers)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d := codeGoalDriver{runtime: r}
	raw, err := d.command(ctx, r.root, *cfg.HostReport, proc.WorkerEnvironment())
	if err != nil {
		return goal.Node{}, err
	}
	return decodeCodeHost(raw, r.main, cfg.Limits, workers, time.Now())
}

func decodeCodeHost(raw []byte, id string, limits goal.Resources, workers map[string]bool, at time.Time) (goal.Node, error) {
	var report struct {
		Cores  *float64 `json:"cpu_cores"`
		Load   *float64 `json:"load_cpu"`
		Memory *int64   `json:"available_memory_mb"`
		Disk   *int64   `json:"available_disk_mb"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 64<<10 || decoder.Decode(&report) != nil || decoder.Decode(new(any)) != io.EOF || report.Cores == nil || report.Load == nil || report.Memory == nil || report.Disk == nil {
		return goal.Node{}, goal.ErrInvalid
	}
	if *report.Cores <= 0 || *report.Load < 0 || *report.Memory < 0 || *report.Disk < 0 || id == "" || limits.CPU <= 0 || limits.MemoryMB <= 0 || limits.DiskMB <= 0 || at.Unix() <= 0 {
		return goal.Node{}, goal.ErrInvalid
	}
	return goal.Node{ID: id, Connected: true, ObservedAt: at.Unix(), CPUCores: min(*report.Cores, float64(runtime.NumCPU())), LoadCPU: *report.Load, AvailableMemoryMB: *report.Memory, AvailableDiskMB: *report.Disk, CPULimit: limits.CPU, MemoryLimitMB: limits.MemoryMB, DiskLimitMB: limits.DiskMB, Workers: maps.Clone(workers)}, nil
}
