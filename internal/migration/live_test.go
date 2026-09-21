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

// A live COPY never stops the VM: the base disks are cloned behind the job's
// snapshot, which is merged back once the copy is verified.
func TestLiveCopyKeepsTheSourceRunning(t *testing.T) {
	h := liveFake(2)
	s := liveRun(t, h, modeCopy)
	if s.Phase != phaseCompleted || s.Error != "" {
		t.Fatalf("live copy did not complete: %s %s", s.Phase, s.Error)
	}
	if hasEvent(h, "shutdown") || h.power[7] != esxi.On {
		t.Fatal("a live copy stopped the source")
	}
	if eventIndex(h, "snapshot-create") > eventIndex(h, "clone:0") || eventIndex(h, "consolidate:7") < eventIndex(h, "clone:1") {
		t.Fatalf("wrong order: %v", h.events)
	}
	if h.snapshot != "Get Snapshot:\n" || strings.Contains(h.files[sourceDir+"/lab.vmx"], "000001") {
		t.Fatal("the source was not left without the snapshot")
	}
	if cfg := h.files[targetVMX]; !strings.Contains(cfg, "\"d1.vmdk\"") || strings.Contains(cfg, "000001") {
		t.Fatalf("the copy does not name its base disks: %s", cfg)
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

// Everything before the target runs is undone: the source runs again, on its
// base disks, without the snapshot.
func TestLiveFailureRestoresTheSource(t *testing.T) {
	for name, tc := range map[string]struct {
		break_ func(*fakeHost)
		source int
	}{
		"clone":        {func(h *fakeHost) { h.cloneFail = 1 }, 7},
		"verification": {func(h *fakeHost) { h.verifyFail = 0 }, 7},
		"registration": {func(h *fakeHost) { h.registerFail = true }, 23},
		"power-on":     {func(h *fakeHost) { h.powerFail = true }, 23},
		"snapshot":     {func(h *fakeHost) { h.snapshotFail = true }, 7},
	} {
		t.Run(name, func(t *testing.T) {
			h := liveFake(2)
			tc.break_(h)
			s := liveRun(t, h, modeMove)
			if s.Phase != phaseRolledBack {
				t.Fatalf("not restored: %s %s %v", s.Phase, s.Error, h.events)
			}
			if h.power[tc.source] != esxi.On {
				t.Fatalf("the source does not run again: %v", h.events)
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
	if _, e := (Analyzer{Host: liveFake(1)}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: modeMove, Live: true}); e == nil {
		t.Fatal("a live move that would leave the VM off was accepted")
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
