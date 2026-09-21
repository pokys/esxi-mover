package migration

import (
	"context"
	"strings"
	"testing"

	"esxi-mover/internal/esxi"
)

const targetVMX = "/vmfs/volumes/target/lab/lab.vmx"

func liveRun(t *testing.T, h *fakeHost, mode string) State {
	t.Helper()
	r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: mode, PowerOn: mode == modeMove, Live: true})
	if e != nil || !r.Ready {
		t.Fatalf("live analysis blocked: %v %+v", e, r.Checks)
	}
	j := NewJob(r)
	(&Engine{h, testOptions()}).Run(context.Background(), j)
	return j.Snapshot()
}
func liveFake(disks int) *fakeHost {
	h := newFake(disks)
	h.power[7] = esxi.On
	return h
}
func eventIndex(h *fakeHost, name string) int {
	for i, e := range h.events {
		if e == name {
			return i
		}
	}
	return -1
}

// A live COPY ends like a cold one: the source is shut down, only at the end,
// and stays off; the copy holds everything up to the shutdown, unregistered
// and without a snapshot.
func TestLiveCopyEndsLikeAColdCopy(t *testing.T) {
	h := liveFake(2)
	s := liveRun(t, h, modeCopy)
	if s.Phase != phaseCompleted || s.Error != "" {
		t.Fatalf("live copy did not complete: %s %s", s.Phase, s.Error)
	}
	order := []string{"snapshot-create", "clone:1", "shutdown", "copy:d1-000001-sesparse.vmdk", "register:" + targetVMX, "consolidate:22", "unregister:22", "consolidate:7"}
	for i := 1; i < len(order); i++ {
		if a, b := eventIndex(h, order[i-1]), eventIndex(h, order[i]); a < 0 || b < a {
			t.Fatalf("%s must come before %s: %v", order[i-1], order[i], h.events)
		}
	}
	if h.power[7] != esxi.Off || hasEvent(h, "power-on:") {
		t.Fatal("the source was started, or never shut down")
	}
	for _, v := range h.vms {
		if v.ID == 22 {
			t.Fatal("the copy stayed registered")
		}
	}
	if cfg := h.files[targetVMX]; !strings.Contains(cfg, "\"d1.vmdk\"") || strings.Contains(cfg, "000001") || h.targetSnapshot != "Get Snapshot:\n" {
		t.Fatalf("the copy was left on its snapshot: %s", cfg)
	}
	if h.snapshot != "Get Snapshot:\n" || strings.Contains(h.files[sourceDir+"/lab.vmx"], "000001") {
		t.Fatal("the source was left on its snapshot")
	}
}

// A live MOVE is down only between the shutdown and the target's power-on,
// and in between it copies just the deltas and the snapshot metadata.
func TestLiveMoveCopiesOnlyTheChanges(t *testing.T) {
	h := liveFake(2)
	s := liveRun(t, h, modeMove)
	if s.Phase != phaseCompleted || s.Error != "" {
		t.Fatalf("live move did not complete: %s %s", s.Phase, s.Error)
	}
	shutdown := eventIndex(h, "shutdown")
	if shutdown < eventIndex(h, "clone:1") {
		t.Fatalf("the source went down before its disks were cloned: %v", h.events)
	}
	for _, f := range []string{"copy:d0-000001.vmdk", "copy:d1-000001-sesparse.vmdk", "copy:lab.vmsd", "copy:lab-Snapshot1.vmsn"} {
		if i := eventIndex(h, f); i < shutdown || i > eventIndex(h, "register:"+targetVMX) {
			t.Fatalf("%s not copied during the cutover: %v", f, h.events)
		}
	}
	if eventIndex(h, "consolidate:22") < eventIndex(h, "power-on:22") || h.power[22] != esxi.On {
		t.Fatalf("the target was not started and merged: %v", h.events)
	}
	cfg := h.files[targetVMX]
	if !strings.Contains(cfg, "\"d0.vmdk\"") || strings.Contains(cfg, "000001") || !strings.Contains(cfg, "uuid.action = \"keep\"") {
		t.Fatalf("the target does not run on merged base disks: %s", cfg)
	}
	for _, v := range h.vms {
		if v.ID == 7 {
			t.Fatal("the source is still registered")
		}
	}
	if !strings.Contains(h.files[sourceDir+"/lab.vmx"], "d0-000001.vmdk") || h.files[sourceDir+"/d0-000001.vmdk"] == "" {
		t.Fatal("the source files were changed after the move")
	}
}

// Everything before a move switches registration is undone: the source is
// registered as before, on its base disks, without the snapshot.
func TestLiveFailureRestoresTheSource(t *testing.T) {
	for name, tc := range map[string]struct {
		break_ func(*fakeHost)
		source int
		power  esxi.Power
	}{
		// Before the shutdown the source simply keeps running.
		"clone":        {func(h *fakeHost) { h.cloneFail = 1 }, 7, esxi.On},
		"verification": {func(h *fakeHost) { h.verifyFail = 0 }, 7, esxi.On},
		"snapshot":     {func(h *fakeHost) { h.snapshotFail = true }, 7, esxi.On},
		// After it, the source stays off: the tool never starts it.
		"cutover":      {func(h *fakeHost) { h.corruptConfig = true }, 7, esxi.Off},
		"registration": {func(h *fakeHost) { h.registerFail = true }, 23, esxi.Off},
	} {
		t.Run(name, func(t *testing.T) {
			h := liveFake(2)
			tc.break_(h)
			s := liveRun(t, h, modeMove)
			if s.Phase != phaseRolledBack {
				t.Fatalf("not restored: %s %s %v", s.Phase, s.Error, h.events)
			}
			if h.power[tc.source] != tc.power || hasEvent(h, "power-on:7") || hasEvent(h, "power-on:23") {
				t.Fatalf("source power is %s, want %s: %v", h.power[tc.source], tc.power, h.events)
			}
			if h.snapshot != "Get Snapshot:\n" || strings.Contains(h.files[sourceDir+"/lab.vmx"], "000001") {
				t.Fatal("the source still runs on the snapshot")
			}
			for _, v := range h.vms {
				if v.ID == 22 {
					t.Fatal("the target stayed registered")
				}
			}
			if h.locked {
				t.Fatal("the operation lock was kept after a clean restore")
			}
		})
	}
}

func TestLiveNeedsARunningVM(t *testing.T) {
	h := newFake(1)
	r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: modeCopy, Live: true})
	if e != nil || r.Ready {
		t.Fatal("a live migration of a stopped VM was accepted")
	}
	// Power-on is the target's own option, live or not.
	if r, e := (Analyzer{Host: liveFake(1)}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: modeMove, Live: true}); e != nil || !r.Ready {
		t.Fatal("a live move without power-on was refused", e)
	}
}

// A snapshot someone else takes during the migration is never merged away:
// the job stops and leaves the tree for the administrator.
func TestLiveNeverMergesAForeignSnapshot(t *testing.T) {
	h := liveFake(1)
	h.foreignSnapshot = true
	s := liveRun(t, h, modeCopy)
	if s.Phase != phaseFailed || !strings.Contains(s.Error, "did not take") {
		t.Fatalf("expected a stop for manual review, got %s %s", s.Phase, s.Error)
	}
	for _, e := range h.events {
		if strings.HasPrefix(e, "consolidate:") {
			t.Fatal("a tree with a foreign snapshot was merged")
		}
	}
}
