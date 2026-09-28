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
	h.files[sourceDir+"/lab.vmx"] += fmt.Sprintf("scsi0:%d.present = \"TRUE\"\nscsi0:%d.fileName = \"[other] data/%s\"\n", slot, slot, name)
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
		offered = offered || strings.Contains(s.Text, "Bring disks from other folders")
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

func TestBringDisksNeedsTheVMOff(t *testing.T) {
	h := newFake(1)
	h.power[7] = esxi.On
	withExternal(h, 1, "data.vmdk")
	r := bring(t, h, true)
	requireAdvice(t, r)
	if r.Ready || len(find(r, "Disks from other folders")) != 2 {
		t.Fatalf("live migration with an outside disk was not blocked: %+v", r.Checks)
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
