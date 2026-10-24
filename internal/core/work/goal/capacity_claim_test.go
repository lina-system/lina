// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"errors"
	"testing"
	"time"
)

func TestClaimRevalidatesQueuedHost(t *testing.T) {
	t.Parallel()
	changes := map[string]func(*Node){
		"disconnected":   func(n *Node) { n.Connected = false },
		"yielding":       func(n *Node) { n.Yielding = true },
		"quota":          func(n *Node) { n.QuotaReset = map[string]int64{"stub": 200} },
		"worker_removed": func(n *Node) { n.Workers = nil },
		"cpu_load":       func(n *Node) { n.LoadCPU = 7 },
		"cpu_cap":        func(n *Node) { n.CPULimit = 1 },
		"memory":         func(n *Node) { n.AvailableMemoryMB = 999 },
		"memory_cap":     func(n *Node) { n.MemoryLimitMB = 999 },
		"disk":           func(n *Node) { n.AvailableDiskMB = 999 },
		"disk_cap":       func(n *Node) { n.DiskLimitMB = 999 },
		"stale":          func(n *Node) {},
	}
	for name, change := range changes {
		for _, restart := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "", true: "/restart"}[restart], func(t *testing.T) {
				e := revisionEngine(t, readyTask("a"))
				old := revisionAction(t, e, "a", Assign)
				n := readyNode("n")
				change(&n)
				if err := e.NodeChanged(t.Context(), n); err != nil {
					t.Fatal(err)
				}
				if got := revisionSnapshot(t, e).Nodes[0]; got.Connected != n.Connected || got.LoadCPU != n.LoadCPU || got.Yielding != n.Yielding {
					t.Fatal("node report was not persisted")
				}
				cfg := e.cfg
				if name == "stale" {
					cfg.Now = func() time.Time { return time.Unix(100, 0).Add(SafetySweep + time.Second) }
				}
				var err error
				if restart {
					cfg.Epoch++
					e, err = NewEngine(cfg)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					e.cfg.Now = cfg.Now
				}
				if err = e.Reconcile(t.Context()); err != nil {
					t.Fatal(err)
				}
				if a, err := e.Claim(t.Context(), old.ID); !errors.Is(err, ErrConflict) {
					t.Fatalf("ineligible host claimed: action=%+v err=%v", a, err)
				}
				s := revisionSnapshot(t, e)
				if s.Actions[0].State != "finished" || s.Goals[0].Tasks[0].Stage != TaskReady || s.Goals[0].Tasks[0].Attempt != 0 {
					t.Fatalf("undispatched reservation not released: %+v", s)
				}
				if err = e.Reconcile(t.Context()); err != nil {
					t.Fatal(err)
				}
				g := revisionSnapshot(t, e).Goals[0]
				reason := "host_capacity"
				if name == "quota" {
					reason = "worker_quota"
				}
				if len(g.Waits) != 1 || g.Waits[0].Reason != reason {
					t.Fatalf("missing capacity wait: %+v", g)
				}
				// A fresh alternative can take an unstarted assignment without
				// consuming an attempt or reusing the retired action identity.
				n = readyNode("other")
				n.ObservedAt = cfg.Now().Unix()
				if err = e.NodeChanged(t.Context(), n); err != nil {
					t.Fatal(err)
				}
				next := revisionAction(t, e, "a", Assign)
				if next.ID == old.ID || next.Attempt != old.Attempt || next.NodeID != "other" {
					t.Fatalf("bad replacement: old=%+v next=%+v", old, next)
				}
				if _, err = e.Claim(t.Context(), next.ID); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestClaimCapacityCountsOtherReservationsOnly(t *testing.T) {
	t.Parallel()
	e := revisionEngine(t, readyTask("a"), readyTask("b"))
	a := revisionAction(t, e, "a", Assign)
	b := revisionAction(t, e, "b", Assign)
	n := readyNode("n")
	n.CPULimit = 4
	if err := e.NodeChanged(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Claim(t.Context(), a.ID); err != nil {
		t.Fatalf("counted own reservation twice: %v", err)
	}
	n.CPULimit = 2
	if err := e.NodeChanged(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Claim(t.Context(), b.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("ignored live sibling reservation: %v", err)
	}
	for _, action := range revisionSnapshot(t, e).Actions {
		if action.ID == a.ID && action.State != "claimed" {
			t.Fatalf("interrupted healthy work: %+v", action)
		}
	}
	if err := e.Report(t.Context(), a.ID, e.cfg.Epoch, Observation{Status: OutcomeSucceeded, Head: "head-a", EffectsSettled: true}); err != nil {
		t.Fatal(err)
	}
	b = revisionAction(t, e, "b", Assign)
	if _, err := e.Claim(t.Context(), b.ID); err != nil {
		t.Fatalf("released slot did not become usable: %v", err)
	}
}

func TestClaimCapacityReturnsRecoveryAllowances(t *testing.T) {
	t.Parallel()
	for _, kind := range []ActionKind{Resume, RepairConflict, ContinueWorker} {
		t.Run(string(kind), func(t *testing.T) {
			e := revisionEngine(t, readyTask("a"))
			switch kind {
			case Resume:
				revisionReport(t, e, "a", Assign, Observation{Status: OutcomeFailed, EffectsSettled: true})
			case RepairConflict:
				revisionReport(t, e, "a", Assign, Observation{Status: OutcomeSucceeded, Head: "head", EffectsSettled: true})
				revisionReport(t, e, "a", InspectPR, revisionPR("head"))
				revisionReport(t, e, "a", Merge, Observation{Status: OutcomeConflict})
			case ContinueWorker:
				revisionReport(t, e, "a", Assign, Observation{Status: OutcomeNoReceipt, Handle: "thread", EffectsSettled: true})
			case Assign, InspectPR, Merge, RunQA, RepairQA, Deploy, Verify, Observe, Decide, PlanGoal, CheckCriterion, ReadSource:
				t.Fatal("unexpected fixture")
			}
			old := revisionAction(t, e, "a", kind)
			n := readyNode("n")
			n.Yielding = true
			if err := e.NodeChanged(t.Context(), n); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Claim(t.Context(), old.ID); !errors.Is(err, ErrConflict) {
				t.Fatalf("claimed recovery on yielding host: %v", err)
			}
			task := revisionSnapshot(t, e).Goals[0].Tasks[0]
			if task.Recoveries != 0 || task.Conflicts != 0 || task.Continuations != 0 || task.Attempt != old.Attempt-1 || task.NodeID != "n" {
				t.Fatalf("lost recovery allowance or affinity: %+v", task)
			}
			e.cfg.Now = func() time.Time { return time.Unix(101, 0) }
			n = readyNode("n")
			n.ObservedAt = 101
			if err := e.NodeChanged(t.Context(), n); err != nil {
				t.Fatal(err)
			}
			next := revisionAction(t, e, "a", kind)
			if next.ID == old.ID || next.Attempt != old.Attempt || next.Handle != old.Handle {
				t.Fatalf("bad recovery replacement: %+v", next)
			}
			if _, err := e.Claim(t.Context(), next.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClaimCapacityPreservesExistingEffectObservation(t *testing.T) {
	t.Parallel()
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "restart"}[restart], func(t *testing.T) {
			e, original := claimedAssignment(t)
			if err := e.Signal(t.Context(), Signal{ID: "new-input", ActionID: original.ID}); err != nil {
				t.Fatal(err)
			}
			if err := e.Report(t.Context(), original.ID, e.cfg.Epoch, Observation{Status: OutcomeRunning, Handle: "live"}); err != nil {
				t.Fatal(err)
			}
			n := readyNode("n")
			n.Yielding = true
			if err := e.NodeChanged(t.Context(), n); err != nil {
				t.Fatal(err)
			}
			if restart {
				cfg := e.cfg
				cfg.Epoch++
				var err error
				e, err = NewEngine(cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
			reads := 0
			e.cfg.Driver = &fixtureDriver{run: func(Action) (Observation, error) {
				t.Error("reexecuted an existing effect")
				return Observation{}, ErrInvalid
			}, observe: func(a Action) (Observation, error) {
				reads++
				if a.ID != original.ID || a.Handle != "live" {
					t.Errorf("wrong effect observed: %+v", a)
				}
				return Observation{Status: OutcomeSucceeded, Head: "head", EffectsSettled: true}, nil
			}}
			s := authorizationStep(t, e)
			task := s.Goals[0].Tasks[0]
			if reads != 1 || task.Stage != TaskDelivered || task.Attempt != original.Attempt {
				t.Fatalf("capacity check released an already-dispatched effect: reads=%d task=%+v", reads, task)
			}
		})
	}
}
