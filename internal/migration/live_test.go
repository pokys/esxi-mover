package migration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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
	h.files[sourceDir+"/lab.vmx"] += "uuid.action = \"keep\"\n"
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
	if strings.Contains(h.files[targetVMX], "uuid.action") {
		t.Fatal("the live copy inherited the source's keep-identity setting")
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

// A failed observation is not proof of clone exit. The base disk must stay
// read-only behind the snapshot, and the host lock must keep new jobs out.
func TestLiveUnknownCloneKeepsSnapshotAndLock(t *testing.T) {
	for _, scenario := range []string{"timeout", "status lost", "launch and status lost"} {
		t.Run(scenario, func(t *testing.T) {
			h := liveFake(1)
			h.cloneHangs = true
			o := testOptions()
			if scenario == "timeout" {
				o.CloneTimeout = 5 * time.Millisecond
			} else {
				h.pollErrors = 1000
				h.launchLost = scenario == "launch and status lost"
			}
			r, err := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: modeMove, Live: true})
			if err != nil || !r.Ready {
				t.Fatalf("analysis blocked: %v %+v", err, r.Checks)
			}
			j := NewJob(r)
			(&Engine{Host: h, Options: o}).Run(context.Background(), j)
			s := j.Snapshot()
			if s.Phase != phaseUnknown || !strings.Contains(s.Error, "clone exit was not confirmed") {
				t.Fatalf("unknown clone did not require review: %+v", s)
			}
			if !h.locked || h.snapshot == "Get Snapshot:\n" || hasEvent(h, "consolidate:") || hasEvent(h, "finish") {
				t.Fatalf("unknown clone lost its protection: %v", h.events)
			}
			if hasEvent(h, "shutdown") || hasEvent(h, "register:") || h.clones != 1 {
				t.Fatalf("unknown clone triggered more migration work: %v", h.events)
			}
		})
	}
}

type cancelAfterCloneHost struct {
	*fakeHost
	cancel context.CancelFunc
}

func (h *cancelAfterCloneHost) StartClone(ctx context.Context, id string, index, vmID int, src, dst string, requireOff bool) error {
	err := h.fakeHost.StartClone(ctx, id, index, vmID, src, dst, requireOff)
	h.cancel()
	return err
}

func TestLiveCancellationKeepsUnconfirmedCloneSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &cancelAfterCloneHost{fakeHost: liveFake(1), cancel: cancel}
	h.cloneHangs = true
	r, err := (Analyzer{Host: h}).Analyze(ctx, Request{VMID: 7, TargetUUID: "target", Mode: modeMove, Live: true})
	if err != nil || !r.Ready {
		t.Fatalf("analysis blocked: %v", err)
	}
	j := NewJob(r)
	(&Engine{Host: h, Options: testOptions()}).Run(ctx, j)
	s := j.Snapshot()
	if s.Phase != phaseUnknown || !h.locked || hasEvent(h.fakeHost, "consolidate:") || !strings.Contains(s.Error, "context canceled") {
		t.Fatalf("cancellation lost clone protection: %+v %v", s, h.events)
	}
}

func TestLiveRequiresSourceSpaceBeforeSnapshot(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, free := range []int64{0, minLiveSourceFree - 1, minLiveSourceFree} {
			h := liveFake(1)
			h.ds[0].Free = free
			r, err := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: modeCopy, Live: live})
			if err != nil {
				t.Fatal(err)
			}
			wantReady := !live || free >= minLiveSourceFree
			if r.Ready != wantReady {
				t.Fatalf("live=%t free=%d ready=%t: %+v", live, free, r.Ready, r.Checks)
			}
		}
	}
}

// Change free-space observations only while the clone is running, so tests
// exercise monitoring after the preflight rather than an initial refusal.
type spaceChangingHost struct {
	*fakeHost
	free                 int64
	spaceErrors          int
	ignoreStop           bool
	completeAfter, polls int
}

func (h *spaceChangingHost) Datastores(ctx context.Context) ([]esxi.Datastore, error) {
	ds, err := h.fakeHost.Datastores(ctx)
	if err != nil {
		return nil, err
	}
	if h.clones > 0 && !h.cloneStopped {
		if h.spaceErrors != 0 {
			if h.spaceErrors > 0 {
				h.spaceErrors--
			}
			return nil, fmt.Errorf("space reading unavailable")
		}
		ds[0].Free = h.free
	}
	return ds, nil
}

func (h *spaceChangingHost) CloneStatus(ctx context.Context, index int) (esxi.CloneStatus, error) {
	h.polls++
	if h.completeAfter > 0 && h.polls >= h.completeAfter {
		h.cloneHangs = false
	}
	return h.fakeHost.CloneStatus(ctx, index)
}

func (h *spaceChangingHost) StopClone(ctx context.Context, index int) error {
	if h.ignoreStop {
		h.event(fmt.Sprintf("stop-clone:%d", index))
		return nil // The request succeeded, but there is still no exit marker.
	}
	return h.fakeHost.StopClone(ctx, index)
}

func TestLiveSpaceMonitoringStopsBeforeRestoring(t *testing.T) {
	for _, scenario := range []string{"space exhausted", "monitor unavailable", "stop unconfirmed", "transient monitor failure"} {
		t.Run(scenario, func(t *testing.T) {
			h := &spaceChangingHost{fakeHost: liveFake(1), free: minLiveSourceFree - 1}
			h.cloneHangs = true
			switch scenario {
			case "monitor unavailable":
				h.spaceErrors = -1
			case "stop unconfirmed":
				h.ignoreStop = true
			case "transient monitor failure":
				h.free, h.spaceErrors, h.completeAfter = 80<<30, 1, 3
			}
			r, err := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: modeCopy, Live: true})
			if err != nil || !r.Ready {
				t.Fatalf("analysis blocked: %v", err)
			}
			j := NewJob(r)
			(&Engine{Host: h, Options: testOptions()}).Run(context.Background(), j)
			s := j.Snapshot()
			if scenario == "transient monitor failure" {
				if s.Phase != phaseCompleted || hasEvent(h.fakeHost, "stop-clone:") {
					t.Fatalf("a transient monitor error stopped the migration: %+v %v", s, h.events)
				}
				return
			}
			if !hasEvent(h.fakeHost, "stop-clone:") || hasEvent(h.fakeHost, "shutdown") {
				t.Fatalf("space guard did not stop the clone before cutover: %v", h.events)
			}
			if h.ignoreStop {
				if s.Phase != phaseUnknown || !h.locked || hasEvent(h.fakeHost, "consolidate:") {
					t.Fatalf("an unconfirmed stop merged the snapshot: %+v %v", s, h.events)
				}
				return
			}
			if s.Phase != phaseRolledBack || h.locked || h.power[7] != esxi.On || h.snapshot != "Get Snapshot:\n" {
				t.Fatalf("source not restored after confirmed clone exit: %+v %v", s, h.events)
			}
			if eventIndex(h.fakeHost, "stop-clone:0") > eventIndex(h.fakeHost, "consolidate:7") {
				t.Fatal("the snapshot was merged before stopping the clone")
			}
		})
	}
}
