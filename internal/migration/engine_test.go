package migration

import (
	"context"
	"fmt"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"
	"time"

	"esxi-mover/internal/esxi"
)

func TestCopyPreservesSource(t *testing.T) {
	h := newFake(2)
	before := map[string]string{}
	for k, v := range h.files {
		before[k] = v
	}
	j, _ := run(t, h, "COPY", false)
	s := j.Snapshot()
	if s.Phase != "completed" || !s.TargetVerified || s.TargetRegistration != "not registered" || len(h.vms) != 1 || h.clones != 2 {
		t.Fatalf("COPY failed: %+v", s)
	}
	if hasEvent(h, "unregister:") || hasEvent(h, "register:") || hasEvent(h, "power-on:") {
		t.Fatal("COPY modified registration")
	}
	for p, v := range before {
		if h.files[p] != v {
			t.Fatal("source file changed", p)
		}
	}
}

// The clone output keeps every disk, not only the one cloned last.
func TestCloneLogKeepsEveryDisk(t *testing.T) {
	h := newFake(2)
	j, _ := run(t, h, "COPY", false)
	log := j.Snapshot().TechnicalLog
	for _, want := range []string{"Disk 1: " + sourceDir + "/d0.vmdk", "Disk 2: " + sourceDir + "/d1.vmdk"} {
		if !strings.Contains(log, want) {
			t.Fatalf("clone output lacks %q:\n%s", want, log)
		}
	}
}
func TestMoveCommitOrderAndPowerOption(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			h := newFake(2)
			h.power[7] = esxi.On
			j, _ := run(t, h, "MOVE", on)
			s := j.Snapshot()
			if s.Phase != "completed" {
				t.Fatalf("MOVE failed: %+v", s)
			}
			unreg := -1
			verified := map[string]bool{}
			config := false
			for i, event := range h.events {
				if strings.HasPrefix(event, "verify:/vmfs/volumes/target/") {
					verified[path.Base(strings.TrimPrefix(event, "verify:"))] = true
				}
				if event == "config:lab.vmx" {
					config = true
				}
				if event == "unregister:7" {
					unreg = i
					if len(verified) != 2 || !config {
						t.Fatal("commit preceded verification")
					}
				}
			}
			if unreg < 0 || !hasEvent(h, "register:/vmfs/volumes/target/") || hasEvent(h, "power-on:") != on || hasEvent(h, "power-on:7") {
				t.Fatal("bad registration/power sequence")
			}
			if s.SourceRegistration != "not registered" || s.TargetRegistration != "registered" {
				t.Fatal(s)
			}
		})
	}
}
func TestAnyVerificationFailureNeverUnregistersSource(t *testing.T) {
	for _, index := range []int{0, 1} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			h := newFake(2)
			h.verifyFail = index
			j, _ := run(t, h, "MOVE", true)
			if j.Snapshot().Phase != "failed" || hasEvent(h, "unregister:") || len(h.vms) != 1 {
				t.Fatal("verification failure crossed commit point")
			}
		})
	}
}
func TestCloneFailureNeverUnregistersSource(t *testing.T) {
	h := newFake(2)
	h.cloneFail = 1
	j, _ := run(t, h, "MOVE", true)
	if j.Snapshot().Phase != "failed" || hasEvent(h, "unregister:") || !h.locked {
		t.Fatal("clone failure mishandled")
	}
}
func TestConfigFailureNeverUnregistersSource(t *testing.T) {
	h := newFake(1)
	h.corruptConfig = true
	j, _ := run(t, h, "MOVE", false)
	if j.Snapshot().Phase != "failed" || hasEvent(h, "unregister:") {
		t.Fatal("config failure crossed commit point")
	}
}
func TestTargetRegisterFailureRestoresSource(t *testing.T) {
	h := newFake(1)
	h.registerFail = true
	j, _ := run(t, h, "MOVE", true)
	if j.Snapshot().SourceRegistration != "registered" || j.Snapshot().Phase != "failed" || len(h.vms) != 1 || h.vms[0].ID != 23 || hasEvent(h, "power-on:") {
		t.Fatalf("source rollback failed: %+v", j.Snapshot())
	}
}
func TestLostMutationRepliesAreReconciled(t *testing.T) {
	h := newFake(1)
	h.registerLost = true
	h.unregisterLost = true
	j, _ := run(t, h, "MOVE", false)
	if j.Snapshot().Phase != "completed" || len(h.vms) != 1 || h.vms[0].ID != 22 {
		t.Fatalf("lost replies: %+v", j.Snapshot())
	}
}
func TestDetachedDisconnectDoesNotRelaunch(t *testing.T) {
	h := newFake(1)
	h.launchLost = true
	h.pollErrors = 2
	j, _ := run(t, h, "COPY", false)
	if j.Snapshot().Phase != "completed" || h.clones != 1 {
		t.Fatalf("clone re-launched or failed: %+v", j.Snapshot())
	}
}
func TestSnapshotsAtEveryBoundary(t *testing.T) {
	for _, at := range []int{2, 4, 5, 6} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			h := newFake(2)
			r := analyze(t, h, "MOVE", false)
			h.snapshotAt = at
			j := NewJob(r)
			(&Engine{h, testOptions()}).Run(context.Background(), j)
			if j.Snapshot().Phase != "failed" || hasEvent(h, "unregister:") {
				t.Fatal("snapshot crossed commit")
			}
			if at <= 5 && h.clones != 0 {
				t.Fatal("snapshot clone started")
			}
		})
	}
}
func TestSnapshotLayersBlockAnalyze(t *testing.T) {
	for name, change := range map[string]func(*fakeHost){"manager": func(h *fakeHost) { h.snapshot = "Get Snapshot:\n|-ROOT" }, "vmx": func(h *fakeHost) {
		h.files[sourceDir+"/lab.vmx"] = strings.ReplaceAll(h.files[sourceDir+"/lab.vmx"], "d0.vmdk", "d0-004512.vmdk")
		h.files[sourceDir+"/d0-004512.vmdk"] = diskText(0, true)
	}, "parent": func(h *fakeHost) {
		h.files[sourceDir+"/d0.vmdk"] = strings.ReplaceAll(diskText(0, true), "parentCID=ffffffff", "parentCID=12345678")
	}, "orphan": func(h *fakeHost) { h.files[sourceDir+"/old-delta.vmdk"] = "orphan" }, "stale-vmsd": func(h *fakeHost) {
		h.files[sourceDir+"/lab.vmsd"] = "snapshot.numSnapshots = \"0\"\nsnapshot0.filename = \"old.vmsn\""
	}, "sesparse": func(h *fakeHost) {
		h.files[sourceDir+"/d0.vmdk"] = strings.ReplaceAll(diskText(0, true), `createType="vmfs"`, `createType="seSparse"`)
	}} {
		t.Run(name, func(t *testing.T) {
			h := newFake(1)
			change(h)
			r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", PowerOn: false, TargetName: ""})
			if e == nil && r.Ready {
				t.Fatal("snapshot accepted")
			}
			requireAdvice(t, r)
		})
	}
}

// A suspend file left behind after a resume still blocks, but the check has to
// tell the operator how to prove it orphaned and what to do then.
func TestSuspendFileBlocksWithAdvice(t *testing.T) {
	for name, tc := range map[string]struct {
		change      func(*fakeHost)
		want, avoid []string
	}{
		"left-over": {func(h *fakeHost) { h.power[7] = esxi.On; h.files[sourceDir+"/lab-1a2b.vmem"] = "memory" },
			[]string{"Leftover suspend file", "vmkfstools -D '" + sourceDir + "/lab-1a2b.vmss'", "'" + sourceDir + "/lab-1a2b.vmem'", "_quarantine"}, nil},
		"no-vmem":   {func(h *fakeHost) { h.power[7] = esxi.On }, []string{"Leftover suspend file"}, []string{".vmem"}},
		"suspended": {func(h *fakeHost) { h.power[7] = esxi.Suspended }, []string{"Power the VM on"}, []string{"vmkfstools", "_quarantine"}},
		"checkpoint": {func(h *fakeHost) {
			h.files[sourceDir+"/lab.vmx"] += "checkpoint.vmState = \"lab-1a2b.vmss\"\n"
		}, []string{"Power the VM on"}, []string{"vmkfstools", "_quarantine"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newFake(1)
			h.files[sourceDir+"/lab-1a2b.vmss"] = "state"
			tc.change(h)
			r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", PowerOn: false, TargetName: ""})
			if e != nil {
				t.Fatal(e)
			}
			if r.Ready || reportStatus(r, "Snapshot artifacts") != "BLOCK" {
				t.Fatal("suspend file accepted")
			}
			shown := ""
			for _, c := range r.Checks {
				if c.Name == "Snapshot artifacts" {
					if strings.Contains(c.Detail, "\n") {
						t.Fatalf("detail is not one line: %q", c.Detail)
					}
					shown = c.Title
					for _, s := range c.Steps {
						shown += "\n" + s.Text + "\n" + s.Command
					}
				}
			}
			for _, w := range tc.want {
				if !strings.Contains(shown, w) {
					t.Fatalf("advice lacks %q: %s", w, shown)
				}
			}
			for _, w := range tc.avoid {
				if strings.Contains(shown, w) {
					t.Fatalf("advice has %q: %s", w, shown)
				}
			}
		})
	}
}

// CBT set on the VM and on every disk is one problem with one fix, so it shows
// as one row that lists every key.
func TestRepeatedProblemIsOneRow(t *testing.T) {
	h := newFake(1)
	h.files[sourceDir+"/lab.vmx"] += "ctkEnabled = \"TRUE\"\nscsi0:0.ctkEnabled = \"TRUE\"\n"
	r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY"})
	if e != nil {
		t.Fatal(e)
	}
	rows := []Check{}
	for _, c := range r.Checks {
		if c.Title == "Changed Block Tracking is on" {
			rows = append(rows, c)
		}
	}
	if r.Ready || len(rows) != 1 {
		t.Fatalf("want one blocking CBT row, got %d: %+v", len(rows), rows)
	}
	if !strings.Contains(rows[0].Detail, "ctkenabled") || !strings.Contains(rows[0].Detail, "scsi0:0.ctkenabled") {
		t.Fatalf("keys missing: %q", rows[0].Detail)
	}
}

// A disk on another datastore is one problem: the row names the device and the
// datastore, and the same VMX line is not reported again as an unknown path.
func TestDiskOnAnotherDatastoreIsNamedOnce(t *testing.T) {
	h := newFake(1)
	h.files[sourceDir+"/lab.vmx"] = strings.ReplaceAll(h.files[sourceDir+"/lab.vmx"], "d0.vmdk", "[target] data/d0.vmdk")
	h.files["/vmfs/volumes/target/data/d0.vmdk"] = diskText(0, true)
	r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY"})
	if e != nil {
		t.Fatal(e)
	}
	requireAdvice(t, r)
	if reportStatus(r, "External configuration reference") != "MISSING" {
		t.Fatal("the misplaced disk was reported twice")
	}
	var row Check
	for _, c := range r.Checks {
		if c.Name == "VMDK location" {
			row = c
		}
	}
	if row.Title != "A disk lies on another datastore" || !strings.Contains(row.Detail, "[target] data/d0.vmdk") {
		t.Fatalf("disk not named: %+v", row)
	}
	copied := false
	for _, s := range row.Steps {
		if strings.Contains(s.Command, "vmkfstools -i '/vmfs/volumes/target/data/d0.vmdk' '"+sourceDir+"/") {
			copied = true
		}
	}
	if !copied {
		t.Fatalf("no copy command with real paths: %+v", row.Steps)
	}
}
func TestUnsafeConfigurationsBlock(t *testing.T) {
	for name, change := range map[string]func(*fakeHost){"suspended": func(h *fakeHost) { h.power[7] = esxi.Suspended }, "vsan": func(h *fakeHost) { h.ds[1].Type = "vsan" }, "rdm": func(h *fakeHost) {
		h.files[sourceDir+"/d0.vmdk"] = strings.ReplaceAll(diskText(0, true), `createType="vmfs"`, `createType="vmfsRawDeviceMap"`)
	}, "multiwriter": func(h *fakeHost) { h.files[sourceDir+"/lab.vmx"] += "scsi0:0.sharing = \"multi-writer\"\n" }, "vtpm": func(h *fakeHost) { h.files[sourceDir+"/lab.vmx"] += "vtpm.present = \"TRUE\"\n" }, "encrypted": func(h *fakeHost) { h.files[sourceDir+"/lab.vmx"] += "encryption.keySafe = \"secret\"\n" }, "cross-datastore": func(h *fakeHost) {
		h.files[sourceDir+"/lab.vmx"] = strings.ReplaceAll(h.files[sourceDir+"/lab.vmx"], "d0.vmdk", "[target] data/d0.vmdk")
	}, "space": func(h *fakeHost) { h.ds[1].Free = 1 }, "existing-operation": func(h *fakeHost) { h.locked = true }, "shared-disk": func(h *fakeHost) {
		h.vms = append(h.vms, esxi.VM{ID: 8, VMXPath: "[source] other/other.vmx"})
		h.files["/vmfs/volumes/source/other/other.vmx"] = "scsi0:0.present = \"TRUE\"\nscsi0:0.fileName = \"" + sourceDir + "/d0.vmdk\"\n"
	}} {
		t.Run(name, func(t *testing.T) {
			h := newFake(1)
			change(h)
			r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "MOVE", PowerOn: false, TargetName: ""})
			if e == nil && r.Ready {
				t.Fatal("unsupported feature accepted")
			}
			requireAdvice(t, r)
		})
	}
}
func TestAllocationFallback(t *testing.T) {
	h := newFake(1)
	h.allocationUnknown = true
	r := analyze(t, h, "COPY", false)
	if r.Disks[0].AllocationKnown || r.Allocated != r.Provisioned || r.Required <= r.Provisioned {
		t.Fatal("unsafe allocation fallback")
	}
}
func TestDatastoreFilesystemAllowlist(t *testing.T) {
	for _, kind := range []string{"VMFS-5", "VMFS-6", "VMFS-L", "VMFS-7", "NFS"} {
		for _, index := range []int{0, 1} {
			t.Run(fmt.Sprintf("store%d_%s", index, kind), func(t *testing.T) {
				h := newFake(1)
				h.ds[index].Type = kind
				r, err := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", PowerOn: false, TargetName: ""})
				if err != nil {
					t.Fatal(err)
				}
				want := kind == "VMFS-5" || kind == "VMFS-6"
				if r.Ready != want {
					t.Fatalf("filesystem %s readiness=%v, want %v", kind, r.Ready, want)
				}
			})
		}
	}
}
func TestPowerOnQuestionAndRollback(t *testing.T) {
	h := newFake(1)
	h.question = "Virtual machine message 19:\nmsg.uuid.altered: moved or copied\n4. I _moved it (I _moved it)\n2. I _copied it (I _copied it) [default]"
	j, _ := run(t, h, "MOVE", true)
	if j.Snapshot().Phase != "completed" || h.lastAnswer != "19:4" {
		t.Fatal("question mishandled", j.Snapshot())
	}
	h = newFake(1)
	h.powerFail = true
	j, e := run(t, h, "MOVE", true)
	if !j.Snapshot().CanRollback || j.Snapshot().TargetRegistration != "registered" {
		t.Fatal("no safe rollback offered")
	}
	if err := e.Rollback(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if j.Snapshot().Phase != "rolled_back" || h.power[23] != esxi.Off {
		t.Fatal("bad rollback")
	}
}
func TestRollbackRefusesRunningTarget(t *testing.T) {
	h := newFake(1)
	h.powerFail = true
	j, e := run(t, h, "MOVE", true)
	h.power[22] = esxi.On
	if e.Rollback(context.Background(), j) == nil || hasEvent(h, "unregister:22") {
		t.Fatal("running target unregistered")
	}
}
func TestForceOffRequiresConfirmation(t *testing.T) {
	h := newFake(1)
	h.power[7] = esxi.On
	h.shutdownStuck = true
	r := analyze(t, h, "COPY", false)
	j := NewJob(r)
	done := make(chan struct{})
	go func() { (&Engine{h, testOptions()}).Run(context.Background(), j); close(done) }()
	deadline := time.Now().Add(time.Second)
	for j.Snapshot().Phase != "awaiting_shutdown" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if j.Control("force", false) == nil {
		t.Fatal("unconfirmed force accepted")
	}
	if err := j.Control("force", true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("job stuck")
	}
	if j.Snapshot().Phase != "completed" || !hasEvent(h, "force-off") {
		t.Fatal(j.Snapshot())
	}
}
func TestSelfMigrationUUID(t *testing.T) {
	if !sameUUID("56 4d 01 02 03 04 05 06-07 08 09 10 11 12 13 14", "02014d56-0403-0605-0708-091011121314") {
		t.Fatal("SMBIOS endianness not handled")
	}
	h := newFake(1)
	r, e := (Analyzer{h, "02014d56-0403-0605-0708-091011121314"}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", PowerOn: false, TargetName: ""})
	if e != nil || r.Ready {
		t.Fatal("self migration accepted")
	}
}
func TestNoDeletionCapability(t *testing.T) {
	host := reflect.TypeOf((*Host)(nil)).Elem()
	for i := 0; i < host.NumMethod(); i++ {
		name := strings.ToLower(host.Method(i).Name)
		if strings.Contains(name, "delete") || strings.Contains(name, "remove") || strings.Contains(name, "cleanup") {
			t.Fatal("deletion API exposed")
		}
	}
	entries, e := os.ReadDir("../esxi")
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		b, e := os.ReadFile("../esxi/" + entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		for _, forbidden := range []string{`"rm"`, `"unlink"`, `"vmsvc/destroy"`, `snapshot.remove`, `"vmkfstools", "-U"`} {
			// The one sanctioned exception: live migration merges the snapshot it
			// took itself, and only after checking the tree holds nothing else.
			if forbidden == `snapshot.remove` && entry.Name() == "snapshot.go" {
				continue
			}
			if strings.Contains(string(b), forbidden) {
				t.Fatalf("source deletion primitive found: %s", entry.Name())
			}
		}
	}
	b, e := os.ReadFile("../esxi/snapshot.go")
	if e != nil || !strings.Contains(string(b), "names[0] != name") {
		t.Fatal("the snapshot merge lost its ownership check")
	}
}
func TestSnapshotClassification(t *testing.T) {
	if SnapshotManager("Get Snapshot:\n") != nil || VMSD("") != nil || VMSD(`snapshot.numSnapshots = "0"`) != nil {
		t.Fatal("empty snapshot rejected")
	}
	for _, s := range []string{"", "unknown", "Get Snapshot:\nerror"} {
		if SnapshotManager(s) == nil {
			t.Fatal("unknown snapshot state accepted")
		}
	}
}

// A real ESXi 6.5 host reports an empty tree as the bare header, and a
// populated one with VMware's own "Desciption" spelling, nested CHILD levels
// and descriptions that themselves span lines. Every populated tree must block.
func TestSnapshotManagerOnRealHostOutput(t *testing.T) {
	if e := SnapshotManager("Get Snapshot:\n"); e != nil {
		t.Fatal("empty tree rejected", e)
	}
	nested := "Get Snapshot:\n" +
		"|-ROOT\n" +
		"--Snapshot Name        : Snapshot 1\n" +
		"--Snapshot Id        : 4\n" +
		"--Snapshot Desciption  : tools, codecs, reader\n" +
		"second line of the description\n" +
		"--Snapshot Created On  : 11/30/2021 12:8:43\n" +
		"--Snapshot State       : powered on\n" +
		"--|-CHILD\n" +
		"----Snapshot Name        : Snapshot 2\n"
	if SnapshotManager(nested) == nil {
		t.Fatal("a populated snapshot tree was not blocked")
	}
}

// A thin disk is cloned thin, so sizing the gate on the provisioned capacity
// refused migrations that comfortably fit. The target still has to be told it
// cannot hold the disks once they grow.
func TestFreeSpaceIsSizedOnAllocationAndWarnsAboutGrowth(t *testing.T) {
	// One disk: 1 GiB provisioned, 512 MiB allocated, so the allocated
	// requirement is about 1.58 GiB and the grown one about 2.15 GiB.
	for _, tc := range []struct {
		free int64
		want string
	}{
		{1 << 30, "BLOCK"},
		{2 << 30, "WARNING"},
		{8 << 30, "OK"},
	} {
		h := newFake(1)
		h.ds[1].Free = tc.free
		r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", PowerOn: false, TargetName: ""})
		if e != nil {
			t.Fatal(e)
		}
		if got := reportStatus(r, "Free space"); got != tc.want {
			t.Fatalf("free %d GiB: wanted %s, got %s (required %d)", tc.free>>30, tc.want, got, r.Required)
		}
	}
}

// A snapshot in an unrelated VM used to block every migration on the host.
// It is only a dependency risk when the chain can reach the source VM.
func TestOtherVMChainBlocksOnlyWhenItReachesTheSource(t *testing.T) {
	const otherDir = "/vmfs/volumes/source/other"
	build := func(parentHint string) *fakeHost {
		h := newFake(1)
		h.vms = append(h.vms, esxi.VM{ID: 8, Name: "other", Datastore: "source", VMXPath: "[source] other/other.vmx"})
		h.files[otherDir+"/other.vmx"] = `.encoding = "UTF-8"
scsi0:0.present = "TRUE"
scsi0:0.fileName = "other-000001.vmdk"
`
		h.files[otherDir+"/other-000001.vmdk"] = `version=1
CID=aaaabbbb
parentCID=1234abcd
parentFileNameHint="` + parentHint + `"
createType="vmfsSparse"
RW 2097152 VMFSSPARSE "other-000001-delta.vmdk"
`
		h.files[otherDir+"/other.vmdk"] = `version=1
CID=1234abcd
parentCID=ffffffff
createType="vmfs"
RW 2097152 VMFS "other-flat.vmdk"
`
		return h
	}
	r, e := (Analyzer{Host: build("other.vmdk")}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", PowerOn: false, TargetName: ""})
	if e != nil {
		t.Fatal(e)
	}
	if got := reportStatus(r, "Shared disk inventory"); got != "OK" {
		t.Fatalf("a chain contained in the other VM's own directory blocked: %s", got)
	}
	r, e = (Analyzer{Host: build("[source] lab/d0.vmdk")}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", PowerOn: false, TargetName: ""})
	if e != nil {
		t.Fatal(e)
	}
	if got := reportStatus(r, "Shared disk inventory"); got != "BLOCK" {
		t.Fatalf("a chain reaching a source disk was allowed: %s", got)
	}
}

// After the last snapshot is deleted a real host leaves the counter behind and
// writes no numSnapshots, which blocked every VM that had ever had a snapshot.
func TestVMSDAcceptsARealEmptyFile(t *testing.T) {
	empty := ".encoding = \"UTF-8\"\nsnapshot.lastUID = \"2032\"\n"
	if e := VMSD(empty); e != nil {
		t.Fatal("an empty VMSD from a real host was rejected:", e)
	}
	for _, s := range []string{
		".encoding = \"UTF-8\"\nsnapshot.numSnapshots = \"1\"\n",
		".encoding = \"UTF-8\"\nsnapshot.lastUID = \"3\"\nsnapshot.uid0.filename = \"vm-000001.vmsn\"\n",
	} {
		if VMSD(s) == nil {
			t.Fatalf("active or stale VMSD metadata accepted: %q", s)
		}
	}
}

// The target folder is named after the source VM's own folder, not after an
// opaque job ID, and an existing folder is never touched.
func TestTargetFolderIsNamedAfterTheSource(t *testing.T) {
	h := newFake(1)
	r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", PowerOn: false, TargetName: ""})
	if e != nil {
		t.Fatal(e)
	}
	if path.Base(r.TargetDir) != "lab" {
		t.Fatalf("target folder is not named after the source: %s", r.TargetDir)
	}
	// An explicit name wins.
	r, e = (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", PowerOn: false, TargetName: "lab-copy"})
	if e != nil {
		t.Fatal(e)
	}
	if path.Base(r.TargetDir) != "lab-copy" {
		t.Fatalf("explicit target folder ignored: %s", r.TargetDir)
	}
	// A name that is not a plain directory name is refused outright.
	for _, bad := range []string{"../escape", "sub/dir", ".hidden"} {
		if _, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", PowerOn: false, TargetName: bad}); e == nil {
			t.Fatalf("unsafe target folder accepted: %q", bad)
		}
	}
}

func TestExistingTargetFolderBlocksAndSuggestsAFreeName(t *testing.T) {
	h := newFake(1)
	h.directories["/vmfs/volumes/target/lab"] = true
	r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", PowerOn: false, TargetName: ""})
	if e != nil {
		t.Fatal(e)
	}
	detail := ""
	for _, c := range r.Checks {
		if c.Name == "Target directory" {
			detail = c.Detail
		}
	}
	if reportStatus(r, "Target directory") != "BLOCK" {
		t.Fatal("an existing target folder was not blocked")
	}
	if !strings.Contains(detail, "lab-2") {
		t.Fatalf("no free name was offered: %q", detail)
	}
}

// The analyzer once produced a target directory the engine refused, and the
// refusal surfaced only after the source VM had been powered off. Preflight
// must settle it.
func TestAnalyzerTargetDirectoryIsOneTheEngineAccepts(t *testing.T) {
	h := newFake(1)
	r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: "COPY", PowerOn: false, TargetName: ""})
	if e != nil {
		t.Fatal(e)
	}
	if !esxi.TargetPath(r.TargetDir) {
		t.Fatalf("the engine would refuse the analyzed target directory: %s", r.TargetDir)
	}
	if reportStatus(r, "Target directory") == "BLOCK" {
		t.Fatal("a usable target directory was blocked")
	}
}

// The exact shape Broadcom documents for vim-cmd vmsvc/message, which a real
// host produced at the end of the first MOVE. The VM reports Powered on while
// the question blocks the boot, so answering it must not be skipped.
func TestMovedQuestionIsAnsweredWhileTheVMReportsPoweredOn(t *testing.T) {
	h := newFake(1)
	h.question = "Virtual machine message 12:\n" +
		"msg.uuid.altered:This virtual machine may have been moved or copied.\n" +
		"\n" +
		"Did you move this virtual machine, or did you copy it?\n" +
		"If you don't know, answer \"I copied it\".\n" +
		"\n" +
		"0. Cancel (Cancel)\n" +
		"1. I _moved it (I _moved it)\n" +
		"2. I _copied it (I _copied it) [default]\n"
	j, _ := run(t, h, "MOVE", true)
	if s := j.Snapshot(); s.Phase != "completed" {
		t.Fatalf("the migration did not complete: %s %s", s.Phase, s.Error)
	}
	if h.lastAnswer != "12:1" {
		t.Fatalf("the moved question was not answered: %q", h.lastAnswer)
	}
}

// A MOVE must not stop at VMware's moved-or-copied question: the target VMX
// says the VM was moved. A COPY keeps the question for the operator.
func TestMoveTargetKeepsIdentityAndCopyDoesNot(t *testing.T) {
	for _, inherited := range []string{"", "uuid.action = \"keep\"\n"} {
		for mode, want := range map[string]string{modeMove: "keep", modeCopy: ""} {
			h := newFake(1)
			h.files[sourceDir+"/lab.vmx"] += inherited
			before := h.files[sourceDir+"/lab.vmx"]
			if got := analyze(t, h, mode, mode == modeMove).TargetConfig["uuid.action"]; got != want {
				t.Fatalf("%s with inherited setting %q: got %q, want %q", mode, inherited, got, want)
			}
			if h.files[sourceDir+"/lab.vmx"] != before {
				t.Fatal("source identity setting was changed")
			}
		}
	}
}

func TestColdCloneTimeoutReportsUnknownOutcome(t *testing.T) {
	h := newFake(1)
	h.cloneHangs = true
	j := NewJob(analyze(t, h, modeCopy, false))
	o := testOptions()
	o.CloneTimeout = 5 * time.Millisecond
	(&Engine{Host: h, Options: o}).Run(context.Background(), j)
	s := j.Snapshot()
	if s.Phase != phaseUnknown || !s.Complete || s.CanStop || !strings.Contains(s.Message, "may still be running") || !h.locked {
		t.Fatalf("unknown clone was presented as stopped: %+v", s)
	}
}
