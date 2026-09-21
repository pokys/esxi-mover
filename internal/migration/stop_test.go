package migration

import (
	"context"
	"testing"
	"time"

	"esxi-mover/internal/esxi"
)

// startJob runs a migration in the background, as the web server does, and
// waits until its clone is running.
func startJob(t *testing.T, h *fakeHost, req Request) (*Job, chan struct{}) {
	t.Helper()
	r, e := (Analyzer{Host: h}).Analyze(context.Background(), req)
	if e != nil || !r.Ready {
		t.Fatalf("analysis blocked: %v %+v", e, r.Checks)
	}
	j := NewJob(r)
	done := make(chan struct{})
	go func() { (&Engine{h, testOptions()}).Run(context.Background(), j); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for j.Snapshot().Phase != phaseCloning {
		if time.Now().After(deadline) {
			t.Fatalf("the clone never started: %+v", j.Snapshot())
		}
		time.Sleep(time.Millisecond)
	}
	return j, done
}
func finished(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stopped job never finished")
	}
}

// A cold stop ends the running clone, registers nothing and releases the lock.
// The source is left alone: a cold migration never starts it by itself.
func TestStopEndsACloneCleanly(t *testing.T) {
	h := newFake(2)
	h.cloneHangs = true
	j, done := startJob(t, h, Request{VMID: 7, TargetUUID: "target", Mode: modeMove})
	if !j.Snapshot().CanStop {
		t.Fatal("a running clone cannot be stopped")
	}
	if j.Control("stop", false) == nil {
		t.Fatal("an unconfirmed stop was accepted")
	}
	if e := j.Control("stop", true); e != nil {
		t.Fatal(e)
	}
	finished(t, done)
	s := j.Snapshot()
	if s.Phase != phaseStopped || s.Error != "" {
		t.Fatalf("not stopped cleanly: %s %s", s.Phase, s.Error)
	}
	if !hasEvent(h, "stop-clone:0") || hasEvent(h, "clone:1") || hasEvent(h, "register:") || hasEvent(h, "unregister:") {
		t.Fatalf("the stop did not end the migration where it was: %v", h.events)
	}
	if h.locked {
		t.Fatal("the operation lock was kept after a clean stop")
	}
}

// A live stop puts the source back: snapshot merged, VM running.
func TestStopRestoresALiveSource(t *testing.T) {
	h := liveFake(1)
	h.cloneHangs = true
	j, done := startJob(t, h, Request{VMID: 7, TargetUUID: "target", Mode: modeMove, PowerOn: true, Live: true})
	if e := j.Control("stop", true); e != nil {
		t.Fatal(e)
	}
	finished(t, done)
	s := j.Snapshot()
	if s.Phase != phaseRolledBack {
		t.Fatalf("not restored: %s %s", s.Phase, s.Error)
	}
	if hasEvent(h, "shutdown") || h.power[7] != esxi.On || h.snapshot != "Get Snapshot:\n" {
		t.Fatalf("the source was not left running without the snapshot: %v", h.events)
	}
}

// Past the point of no return a stop is refused rather than ignored.
func TestStopIsRefusedPastThePointOfNoReturn(t *testing.T) {
	j := NewJob(analyze(t, newFake(1), "MOVE", false))
	if e := j.proceed(phaseCutover, "cutover"); e != nil {
		t.Fatal(e)
	}
	// Even a phase that is stoppable on its own, such as the shutdown inside a
	// live cutover, stays unstoppable once the job has gone past that point.
	j.phase(phaseShutdown, "shutting down")
	if j.Snapshot().CanStop || j.Control("stop", true) == nil {
		t.Fatal("a stop was offered past the point of no return")
	}
	k := NewJob(analyze(t, newFake(1), "MOVE", false))
	if e := k.Control("stop", true); e != nil {
		t.Fatal(e)
	}
	if k.proceed(phaseVerifyingConfig, "config") == nil {
		t.Fatal("an accepted stop was ignored at the boundary")
	}
}
