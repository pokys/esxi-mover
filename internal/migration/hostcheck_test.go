package migration

import (
	"context"
	"strings"
	"testing"

	"esxi-mover/internal/esxi"
)

// Checks that read the running host: VMware Tools and autostart.

// A running VM without VMware Tools can still be migrated, but the analysis
// says up front that the shutdown will wait for the operator.
func TestMissingToolsWarns(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*fakeHost)
		status string
		live   bool
	}{
		"running":           {func(h *fakeHost) {}, statusOK, false},
		"not running":       {func(h *fakeHost) { h.noTools = true }, statusWarning, false},
		"unknown":           {func(h *fakeHost) { h.toolsUnknown = true }, statusWarning, false},
		"not running, live": {func(h *fakeHost) { h.noTools = true }, statusWarning, true},
	} {
		t.Run(name, func(t *testing.T) {
			h := newFake(1)
			h.power[7] = esxi.On
			tc.change(h)
			r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", Live: tc.live})
			if e != nil {
				t.Fatal(e)
			}
			rows := find(r, "VMware Tools")
			if len(rows) != 1 || rows[0].Status != tc.status || !r.Ready {
				t.Fatalf("want one %s row and a ready report: %+v", tc.status, r.Checks)
			}
			if tc.status == statusWarning && (rows[0].Why == "" || len(rows[0].Steps) == 0) {
				t.Fatalf("warning without advice: %+v", rows[0])
			}
			if tc.live && !strings.Contains(rows[0].Why, "snapshot") {
				t.Fatalf("live advice does not mention the snapshot: %q", rows[0].Why)
			}
		})
	}
	h := newFake(1)
	r, _ := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY"})
	if len(find(r, "VMware Tools")) != 0 {
		t.Fatal("a VM that is already off needs no Tools check")
	}
}

// A switch registers the target as a new VM, which the host's autostart does
// not know; the analysis notes it and the finished job reminds of it.
func TestSwitchRemindsOfAutostart(t *testing.T) {
	for name, tc := range map[string]struct {
		mode   string
		action string
		warned bool
	}{
		"switch, on":    {modeMove, "powerOn", true},
		"switch, off":   {modeMove, "none", false},
		"copy only, on": {modeCopy, "powerOn", false},
	} {
		t.Run(name, func(t *testing.T) {
			h := newFake(1)
			h.autostart = map[int]esxi.AutoStart{7: {Order: 2, Action: tc.action}}
			r := analyze(t, h, tc.mode, false)
			// A note among the checks, never a warning: it does not endanger
			// the migration.
			rows := find(r, "Autostart")
			if (len(rows) == 1 && rows[0].Status == statusOK) != tc.warned || (!tc.warned && len(rows) != 0) {
				t.Fatalf("autostart note = %v, want %t", rows, tc.warned)
			}
			j := NewJob(r)
			(&Engine{h, testOptions()}).Run(context.Background(), j)
			s := j.Snapshot()
			if s.Phase != phaseCompleted || strings.Contains(s.Message, "position 2") != tc.warned {
				t.Fatalf("final message: %s %q", s.Phase, s.Message)
			}
		})
	}
}
