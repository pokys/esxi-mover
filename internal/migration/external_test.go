package migration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"esxi-mover/internal/esxi"
)

const otherDir = "/vmfs/volumes/other/data"

// withExternal adds a third datastore and attaches disk name from its data
// folder to the lab VM on scsi0:slot.
func withExternal(h *fakeHost, slot int, name string) {
	h.ds = append(h.ds, esxi.Datastore{Name: "other", UUID: "other", Mount: "/vmfs/volumes/other", Type: "VMFS-6", Mounted: true, Size: 100 << 30, Free: 80 << 30})
	// A real host writes the full path of a disk from another datastore.
	h.files[sourceDir+"/lab.vmx"] += fmt.Sprintf("scsi0:%d.present = \"TRUE\"\nscsi0:%d.fileName = \"%s/%s\"\n", slot, slot, otherDir, name)
	stem := strings.TrimSuffix(name, ".vmdk")
	h.files[otherDir+"/"+name] = strings.Replace(diskText(0, true), "d0-flat.vmdk", stem+"-flat.vmdk", 1)
	h.sizes[otherDir+"/"+stem+"-flat.vmdk"] = 1 << 30
	h.directories[otherDir] = true
}
func bring(t *testing.T, h *fakeHost, live bool) Report {
	t.Helper()
	r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", BringDisks: true, Live: live})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func find(r Report, name string) []Check {
	out := []Check{}
	for _, c := range r.Checks {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func TestBringDisksIsOffByDefault(t *testing.T) {
	h := newFake(1)
	withExternal(h, 1, "data.vmdk")
	r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY"})
	if e != nil {
		t.Fatal(e)
	}
	requireAdvice(t, r)
	rows := find(r, "VMDK location")
	if r.Ready || len(rows) != 1 || rows[0].Title != "A disk lies on another datastore" {
		t.Fatalf("the outside disk was not blocked: %+v", r.Checks)
	}
	offered := false
	for _, s := range rows[0].Steps {
		offered = offered || strings.Contains(s.Text, "Include disks outside the VM folder")
	}
	if !offered {
		t.Fatalf("the option was not offered: %+v", rows[0].Steps)
	}
}

// A disk from elsewhere named like one next to the VMX gets a free name, the
// target VMX points at it, and the source files are never touched.
func TestBringDisksCopiesIntoTheTargetFolder(t *testing.T) {
	h := newFake(1)
	withExternal(h, 1, "d0.vmdk")
	r := bring(t, h, false)
	if !r.Ready {
		t.Fatalf("blocked: %+v", r.Checks)
	}
	if len(r.Disks) != 2 || !r.Disks[1].External || r.Disks[1].Source != otherDir+"/d0.vmdk" || r.Disks[1].Target != "/vmfs/volumes/target/lab/d0_1.vmdk" {
		t.Fatalf("wrong plan: %+v", r.Disks)
	}
	if r.TargetConfig["scsi0:1.filename"] != "d0_1.vmdk" || r.TargetConfig["scsi0:0.filename"] != "d0.vmdk" {
		t.Fatalf("target VMX not rewritten: %v", r.TargetConfig)
	}
	warned := find(r, "Disks from other folders")
	if len(warned) != 1 || warned[0].Status != statusWarning || !strings.Contains(warned[0].Detail, "as d0_1.vmdk") {
		t.Fatalf("no warning naming the new name: %+v", warned)
	}
	before := map[string]string{}
	for k, v := range h.files {
		before[k] = v
	}
	j := NewJob(r)
	(&Engine{h, testOptions()}).Run(context.Background(), j)
	if s := j.Snapshot(); s.Phase != phaseCompleted {
		t.Fatalf("migration failed: %+v", s)
	}
	for p, v := range before {
		if h.files[p] != v {
			t.Fatalf("source file changed: %s", p)
		}
	}
	if !strings.Contains(h.files["/vmfs/volumes/target/lab/lab.vmx"], "\"d0_1.vmdk\"") {
		t.Fatalf("target VMX does not name the copy: %s", h.files["/vmfs/volumes/target/lab/lab.vmx"])
	}
}

// Live, the delta of a disk from another folder lies next to that disk. It is
// copied into the target folder, and the target's snapshot list names the
// copy, never the source disk, so merging on the target cannot reach the source.
func TestLiveBringsDisksFromOtherFolders(t *testing.T) {
	for _, mode := range []string{modeCopy, modeMove} {
		t.Run(mode, func(t *testing.T) {
			h := newFake(1)
			h.power[7] = esxi.On
			withExternal(h, 1, "data.vmdk")
			r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: mode, PowerOn: mode == modeMove, Live: true, BringDisks: true})
			if e != nil || !r.Ready {
				t.Fatalf("live analysis blocked: %v %+v", e, r.Checks)
			}
			j := NewJob(r)
			(&Engine{h, testOptions()}).Run(context.Background(), j)
			if s := j.Snapshot(); s.Phase != phaseCompleted || s.Error != "" {
				t.Fatalf("live migration failed: %s %s", s.Phase, s.Error)
			}
			if eventIndex(h, "copy:data-000001.vmdk") < eventIndex(h, "shutdown") || eventIndex(h, "copy:data-000001-sesparse.vmdk") < 0 {
				t.Fatalf("the outside delta was not copied after the shutdown: %v", h.events)
			}
			for f, text := range h.files {
				if strings.HasPrefix(f, "/vmfs/volumes/target/") && strings.Contains(text, otherDir) {
					t.Fatalf("%s on the target still names the source disk:\n%s", f, text)
				}
			}
			if cfg := h.files["/vmfs/volumes/target/lab/lab.vmx"]; !strings.Contains(cfg, "\"data.vmdk\"") || strings.Contains(cfg, "000001") {
				t.Fatalf("the target does not run on its merged copy: %s", cfg)
			}
		})
	}
}

// A delta finds its parent by file name, so a disk renamed in the target
// folder cannot be migrated live.
func TestLiveBlocksARenamedOutsideDisk(t *testing.T) {
	h := newFake(1)
	h.power[7] = esxi.On
	withExternal(h, 1, "d0.vmdk")
	r := bring(t, h, true)
	requireAdvice(t, r)
	blocked := false
	for _, c := range find(r, "Disks from other folders") {
		blocked = blocked || c.Status == statusBlock
	}
	if r.Ready || !blocked {
		t.Fatalf("a renamed outside disk was accepted live: %+v", r.Checks)
	}
}

// The delta of a disk from another folder grows on that disk's datastore, so
// its free space counts as much as the VM's own.
func TestLiveNeedsSpaceWhereEveryDeltaGrows(t *testing.T) {
	h := newFake(1)
	h.power[7] = esxi.On
	withExternal(h, 1, "data.vmdk")
	h.ds[2].Free = 100 << 20
	r := bring(t, h, true)
	if r.Ready || reportStatus(r, "Source free space") != statusBlock {
		t.Fatalf("a nearly full datastore under an outside disk was accepted: %+v", r.Checks)
	}
}

// The folder a disk comes from is checked too: its own snapshot files and any
// VMX there that uses it block, other VMs' files only warn.
func TestBringDisksChecksTheDiskFolder(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*fakeHost)
		check  string
		status string
	}{
		"own delta":       {func(h *fakeHost) { h.files[otherDir+"/data-000001.vmdk"] = "delta" }, "Disk folder", statusBlock},
		"own sesparse":    {func(h *fakeHost) { h.files[otherDir+"/data-000002-sesparse.vmdk"] = "delta" }, "Disk folder", statusBlock},
		"foreign delta":   {func(h *fakeHost) { h.files[otherDir+"/else-000001.vmdk"] = "delta" }, "Disk folder", statusWarning},
		"foreign suspend": {func(h *fakeHost) { h.files[otherDir+"/else.vmss"] = "state" }, "Disk folder", statusWarning},
		"unregistered vmx": {func(h *fakeHost) {
			h.files[otherDir+"/else.vmx"] = "scsi0:0.present = \"TRUE\"\nscsi0:0.fileName = \"data.vmdk\"\n"
		}, "Disk folder", statusBlock},
		"unreadable vmx": {func(h *fakeHost) { h.files[otherDir+"/else.vmx"] = "scsi0:0.fileName = \"[gone] x/data.vmdk\"\n" }, "Disk folder", statusBlock},
		"unrelated vmx": {func(h *fakeHost) {
			h.files[otherDir+"/else.vmx"] = "scsi0:0.present = \"TRUE\"\nscsi0:0.fileName = \"else.vmdk\"\n"
		}, "Disk folder", statusWarning},
		"registered vmx": {func(h *fakeHost) {
			h.vms = append(h.vms, esxi.VM{ID: 8, VMXPath: "[other] data/else.vmx"})
			h.files[otherDir+"/else.vmx"] = "scsi0:0.present = \"TRUE\"\nscsi0:0.fileName = \"data.vmdk\"\n"
		}, "Shared disk inventory", statusBlock},
	} {
		t.Run(name, func(t *testing.T) {
			h := newFake(1)
			withExternal(h, 1, "data.vmdk")
			tc.change(h)
			r := bring(t, h, false)
			requireAdvice(t, r)
			found := false
			for _, c := range find(r, tc.check) {
				found = found || c.Status == tc.status
			}
			if !found {
				t.Fatalf("want %s %s: %+v", tc.check, tc.status, r.Checks)
			}
			if (tc.status == statusBlock) == r.Ready {
				t.Fatalf("ready=%t with %s %s", r.Ready, tc.check, tc.status)
			}
		})
	}
}

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
// not know; the analysis and the finished job both say so.
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
			rows := find(r, "Autostart")
			if (len(rows) == 1 && rows[0].Status == statusWarning) != tc.warned || (!tc.warned && len(rows) != 0) {
				t.Fatalf("autostart warning = %v, want %t", rows, tc.warned)
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

// Snapshot advice for a file in the disk's folder names that folder, not the
// VM folder, and looks for references in both.
func TestArtifactAdviceUsesTheDiskFolder(t *testing.T) {
	h := newFake(1)
	withExternal(h, 1, "data.vmdk")
	h.files[otherDir+"/data-000001.vmdk"] = "delta"
	r := bring(t, h, false)
	rows := find(r, "Disk folder")
	var row Check
	for _, c := range rows {
		if c.Status == statusBlock {
			row = c
		}
	}
	shown := ""
	for _, s := range row.Steps {
		shown += s.Command + "\n"
	}
	for _, want := range []string{"vmkfstools -D '" + otherDir + "/data-000001.vmdk'", "'" + otherDir + "'/*.vmx", "'" + sourceDir + "'/*.vmx", "/vmfs/volumes/other/_quarantine/data"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("advice lacks %q:\n%s", want, shown)
		}
	}
}
