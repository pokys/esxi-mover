package migration

import (
	"context"
	"fmt"
	"os"
	"path"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"esxi-mover/internal/esxi"
)

type fakeHost struct {
	mu                                                                                                                 sync.Mutex
	files                                                                                                              map[string]string
	sizes                                                                                                              map[string]int64
	directories                                                                                                        map[string]bool
	vms                                                                                                                []esxi.VM
	ds                                                                                                                 []esxi.Datastore
	power                                                                                                              map[int]esxi.Power
	events                                                                                                             []string
	snapshot                                                                                                           string
	locked                                                                                                             bool
	inventoryCalls, snapshotAt                                                                                         int
	cloneFail, verifyFail                                                                                              int
	clones                                                                                                             int
	registerFail, registerLost, unregisterLost, shutdownStuck, powerFail, corruptConfig, launchLost, allocationUnknown bool
	question                                                                                                           string
	lastAnswer                                                                                                         string
	pollErrors                                                                                                         int
	snapshotFail, foreignSnapshot, cloneHangs, cloneStopped                                                           bool
}

const sourceDir = "/vmfs/volumes/source/lab"

func diskText(i int, thin bool) string {
	n := "0"
	if thin {
		n = "1"
	}
	return fmt.Sprintf("version=1\nCID=1234abcd\nparentCID=ffffffff\ncreateType=\"vmfs\"\nRW 2097152 VMFS \"d%d-flat.vmdk\"\nddb.thinProvisioned = \"%s\"\n", i, n)
}
func newFake(disks int) *fakeHost {
	h := &fakeHost{files: map[string]string{}, sizes: map[string]int64{}, directories: map[string]bool{}, power: map[int]esxi.Power{7: esxi.Off}, cloneFail: -1, verifyFail: -1, snapshot: "Get Snapshot:\n"}
	h.ds = []esxi.Datastore{{Name: "source", UUID: "source", Mount: "/vmfs/volumes/source", Type: "VMFS-6", Mounted: true, Size: 100 << 30, Free: 80 << 30}, {Name: "target", UUID: "target", Mount: "/vmfs/volumes/target", Type: "VMFS-6", Mounted: true, Size: 100 << 30, Free: 80 << 30}}
	h.vms = []esxi.VM{{ID: 7, Name: "lab", Datastore: "source", VMXPath: "[source] lab/lab.vmx"}}
	config := ".encoding = \"UTF-8\"\ndisplayName = \"lab\"\nuuid.bios = \"56 4d 01 02 03 04 05 06-07 08 09 10 11 12 13 14\"\nscsi0.present = \"TRUE\"\nscsi0.virtualDev = \"pvscsi\"\nnvram = \"lab.nvram\"\n"
	for i := 0; i < disks; i++ {
		config += fmt.Sprintf("scsi0:%d.present = \"TRUE\"\nscsi0:%d.fileName = \"d%d.vmdk\"\n", i, i, i)
		h.files[fmt.Sprintf("%s/d%d.vmdk", sourceDir, i)] = diskText(i, true)
		h.sizes[fmt.Sprintf("%s/d%d-flat.vmdk", sourceDir, i)] = 1 << 30
	}
	h.files[sourceDir+"/lab.vmx"] = config
	h.files[sourceDir+"/lab.nvram"] = "\x00binary-nvram\xff"
	h.directories[sourceDir] = true
	return h
}
func (h *fakeHost) event(s string) { h.events = append(h.events, s) }
func (h *fakeHost) Inventory(context.Context) (esxi.Inventory, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inventoryCalls++
	if h.snapshotAt > 0 && h.inventoryCalls >= h.snapshotAt {
		h.snapshot = "Get Snapshot:\n|-ROOT\n--Snapshot Name : changed"
	}
	existing := ""
	if h.locked {
		existing = "Existing ESXi Mover operation detected"
	}
	return esxi.Inventory{Capabilities: esxi.Capabilities{Version: "VMware ESXi 6.7.0", Supported: true}, VMs: append([]esxi.VM(nil), h.vms...), Datastores: append([]esxi.Datastore(nil), h.ds...), ExistingOperation: existing}, nil
}
func (h *fakeHost) VMs(context.Context) ([]esxi.VM, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]esxi.VM(nil), h.vms...), nil
}
func (h *fakeHost) Datastores(context.Context) ([]esxi.Datastore, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]esxi.Datastore(nil), h.ds...), nil
}
func (h *fakeHost) ReadFile(_ context.Context, p string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.files[p]
	if !ok {
		return "", fmt.Errorf("file missing")
	}
	return s, nil
}
func (h *fakeHost) Exists(_ context.Context, p string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, f := h.files[p]
	_, s := h.sizes[p]
	return f || s || h.directories[p], nil
}
func (h *fakeHost) Canonical(_ context.Context, p string) (string, error) { return p, nil }
func (h *fakeHost) List(_ context.Context, dir string) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []string{}
	for p := range h.files {
		if path.Dir(p) == dir {
			out = append(out, p)
		}
	}
	return out, nil
}
func (h *fakeHost) Size(_ context.Context, p string) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, ok := h.sizes[p]
	if !ok {
		if s, ok := h.files[p]; ok {
			return int64(len(s)), nil
		}
		return 0, fmt.Errorf("extent missing")
	}
	return n, nil
}
func (h *fakeHost) Allocated(context.Context, string) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.allocationUnknown {
		return 0, fmt.Errorf("unknown allocation")
	}
	return 512 << 20, nil
}
func (h *fakeHost) Power(_ context.Context, id int) (esxi.Power, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.power[id]
	if !ok {
		return "", fmt.Errorf("VMID missing")
	}
	return p, nil
}
func (h *fakeHost) Snapshot(context.Context, int) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshot, nil
}
func (h *fakeHost) Shutdown(_ context.Context, id int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.event("shutdown")
	if !h.shutdownStuck {
		h.power[id] = esxi.Off
	}
	return nil
}
func (h *fakeHost) ForceOff(_ context.Context, id int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.event("force-off")
	h.power[id] = esxi.Off
	return nil
}
func (h *fakeHost) VerifyChain(_ context.Context, p string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.event("verify:" + p)
	if strings.Contains(p, "/target/") && strings.HasSuffix(p, fmt.Sprintf("/d%d.vmdk", h.verifyFail)) {
		return fmt.Errorf("inconsistent target chain")
	}
	return nil
}
func (h *fakeHost) Acquire(_ context.Context, id, meta string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.locked {
		return fmt.Errorf("already locked")
	}
	h.locked = true
	h.event("lock")
	return nil
}
func (h *fakeHost) Finish(context.Context, string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.locked = false
	h.event("finish")
	return nil
}
func (h *fakeHost) CreateTarget(_ context.Context, p string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.directories[p] {
		return fmt.Errorf("target exists")
	}
	h.directories[p] = true
	h.event("mkdir-target")
	return nil
}
func (h *fakeHost) WriteTarget(_ context.Context, dir, name string, data []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !strings.HasPrefix(dir, "/vmfs/volumes/target/") {
		return fmt.Errorf("attempted source write")
	}
	h.files[path.Join(dir, name)] = string(data)
	if h.corruptConfig && strings.HasSuffix(name, ".vmx") {
		h.files[path.Join(dir, name)] += "broken"
	}
	h.event("config:" + name)
	return nil
}
func (h *fakeHost) StartClone(_ context.Context, id string, index, vmID int, src, dst string, requireOff bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if requireOff && h.power[vmID] != esxi.Off {
		return fmt.Errorf("source not off")
	}
	h.clones++
	h.event(fmt.Sprintf("clone:%d", index))
	if index != h.cloneFail {
		h.files[dst] = diskText(index, true)
		h.sizes[path.Join(path.Dir(dst), fmt.Sprintf("d%d-flat.vmdk", index))] = 1 << 30
	}
	if h.launchLost {
		return fmt.Errorf("lost launch reply")
	}
	return nil
}
func (h *fakeHost) CloneStatus(_ context.Context, index int) (esxi.CloneStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pollErrors > 0 {
		h.pollErrors--
		return esxi.CloneStatus{}, fmt.Errorf("temporary connection loss")
	}
	if h.cloneHangs {
		if !h.cloneStopped {
			return esxi.CloneStatus{Alive: true, Progress: 50, Log: "Clone: 50% done."}, nil
		}
		return esxi.CloneStatus{Done: true, ExitCode: 143, Progress: 50}, nil
	}
	code := 0
	if index == h.cloneFail {
		code = 1
	}
	return esxi.CloneStatus{Done: true, ExitCode: code, Progress: 100, Log: "Clone: 100% done."}, nil
}
func (h *fakeHost) StopClone(_ context.Context, index int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.event(fmt.Sprintf("stop-clone:%d", index))
	h.cloneStopped = true
	return nil
}
func (h *fakeHost) Unregister(_ context.Context, id int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.event(fmt.Sprintf("unregister:%d", id))
	for i, v := range h.vms {
		if v.ID == id {
			h.vms = append(h.vms[:i], h.vms[i+1:]...)
			break
		}
	}
	if h.unregisterLost {
		return fmt.Errorf("reply lost")
	}
	return nil
}
func (h *fakeHost) Register(_ context.Context, p string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.event("register:" + p)
	target := strings.Contains(p, "/target/")
	if h.registerFail && target {
		return 0, fmt.Errorf("target registration rejected")
	}
	id := 22
	if !target {
		id = 23
	}
	h.vms = append(h.vms, esxi.VM{ID: id, Name: "lab", VMXPath: p})
	h.power[id] = esxi.Off
	if target && h.registerLost {
		return 0, fmt.Errorf("register reply lost")
	}
	return id, nil
}
func (h *fakeHost) PowerOn(_ context.Context, id int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.event(fmt.Sprintf("power-on:%d", id))
	// Only the target fails to start; a restored source must still come up.
	if h.powerFail && id == 22 {
		return fmt.Errorf("power-on failure")
	}
	// A real host reports Powered on even while a question blocks the boot.
	h.power[id] = esxi.On
	return nil
}
func (h *fakeHost) Message(context.Context, int) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.question == "" {
		return "No message.", nil
	}
	return h.question, nil
}
func (h *fakeHost) Answer(_ context.Context, id int, message, choice string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastAnswer = message + ":" + choice
	h.question = ""
	h.power[id] = esxi.On
	return nil
}
// vmxOf finds the VMX file behind a registered VM.
func (h *fakeHost) vmxOf(id int) string {
	for _, v := range h.vms {
		if v.ID == id {
			return strings.Replace(v.VMXPath, "[source] ", "/vmfs/volumes/source/", 1)
		}
	}
	return ""
}

// CreateSnapshot does what ESXi does: every disk moves onto a seSparse delta
// whose parent is the base disk, and VMSD and VMSN describe the snapshot.
func (h *fakeHost) CreateSnapshot(_ context.Context, id int, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.event("snapshot-create")
	if h.snapshotFail {
		return fmt.Errorf("snapshot failed")
	}
	p := h.vmxOf(id)
	dir := path.Dir(p)
	cfg := h.files[p]
	vmsd := ".encoding = \"UTF-8\"\nsnapshot.numSnapshots = \"1\"\nsnapshot0.filename = \"lab-Snapshot1.vmsn\"\nsnapshot0.displayName = \"" + name + "\"\n"
	n := 0
	for ; strings.Contains(cfg, fmt.Sprintf("\"d%d.vmdk\"", n)); n++ {
		cfg = strings.Replace(cfg, fmt.Sprintf("\"d%d.vmdk\"", n), fmt.Sprintf("\"d%d-000001.vmdk\"", n), 1)
		h.files[fmt.Sprintf("%s/d%d-000001.vmdk", dir, n)] = fmt.Sprintf("version=1\nCID=5678abcd\nparentCID=1234abcd\ncreateType=\"seSparse\"\nparentFileNameHint=\"d%d.vmdk\"\nRW 2097152 SESPARSE \"d%d-000001-sesparse.vmdk\"\n", n, n)
		h.sizes[fmt.Sprintf("%s/d%d-000001-sesparse.vmdk", dir, n)] = 4 << 20
		vmsd += fmt.Sprintf("snapshot0.disk%d.fileName = \"d%d.vmdk\"\n", n, n)
	}
	h.files[p] = cfg
	h.files[dir+"/lab.vmsd"] = vmsd + fmt.Sprintf("snapshot0.numDisks = \"%d\"\n", n)
	h.sizes[dir+"/lab-Snapshot1.vmsn"] = 32 << 10
	h.snapshot = "Get Snapshot:\n|-ROOT\n--Snapshot Name        : " + name + "\n--Snapshot Id        : 1\n"
	if h.foreignSnapshot {
		h.snapshot += "----Snapshot Name        : nightly-backup\n"
	}
	return nil
}

// ConsolidateOwnSnapshot merges like ESXi: the VMX names the base disks again
// and the deltas and snapshot state are gone.
func (h *fakeHost) ConsolidateOwnSnapshot(_ context.Context, id int, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.event(fmt.Sprintf("consolidate:%d", id))
	if !strings.Contains(h.snapshot, ": "+name+"\n") {
		return fmt.Errorf("not the job's snapshot")
	}
	p := h.vmxOf(id)
	dir := path.Dir(p)
	h.files[p] = strings.ReplaceAll(h.files[p], "-000001.vmdk\"", ".vmdk\"")
	for k := range h.files {
		if path.Dir(k) == dir && strings.Contains(k, "-000001") {
			delete(h.files, k)
		}
	}
	for k := range h.sizes {
		if path.Dir(k) == dir && (strings.Contains(k, "-000001") || strings.HasSuffix(k, ".vmsn")) {
			delete(h.sizes, k)
		}
	}
	h.files[dir+"/lab.vmsd"] = ".encoding = \"UTF-8\"\n"
	h.snapshot = "Get Snapshot:\n"
	return nil
}
func (h *fakeHost) CopyToTarget(_ context.Context, src, dir string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !strings.HasPrefix(dir, "/vmfs/volumes/target/") {
		return fmt.Errorf("attempted source write")
	}
	dst := path.Join(dir, path.Base(src))
	if _, ok := h.files[dst]; ok {
		return fmt.Errorf("target file exists")
	}
	s, isFile := h.files[src]
	n, isSized := h.sizes[src]
	if !isFile && !isSized {
		return fmt.Errorf("source file missing")
	}
	if isFile {
		h.files[dst] = s
	}
	if isSized {
		h.sizes[dst] = n
	}
	h.event("copy:" + path.Base(src))
	return nil
}
func testOptions() Options {
	o := DefaultOptions()
	o.PollInterval = time.Millisecond
	o.ShutdownTimeout = 3 * time.Millisecond
	o.DisconnectTimeout = 20 * time.Millisecond
	o.PowerOnTimeout = 5 * time.Millisecond
	return o
}
func analyze(t *testing.T, h *fakeHost, mode string, on bool) Report {
	t.Helper()
	r, e := (Analyzer{Host: h}).Analyze(context.Background(), Request{VMID: 7, TargetUUID: "target", Mode: mode, PowerOn: on, TargetName: ""})
	if e != nil {
		t.Fatal(e)
	}
	if !r.Ready {
		t.Fatalf("fixture blocked: %+v", r.Checks)
	}
	return r
}
func run(t *testing.T, h *fakeHost, mode string, on bool) (*Job, *Engine) {
	t.Helper()
	r := analyze(t, h, mode, on)
	j := NewJob(r)
	e := &Engine{h, testOptions()}
	e.Run(context.Background(), j)
	return j, e
}
func hasEvent(h *fakeHost, prefix string) bool {
	for _, e := range h.events {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

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
		})
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

func reportStatus(r Report, name string) string {
	status := "MISSING"
	for _, c := range r.Checks {
		if c.Name != name {
			continue
		}
		if c.Status == "BLOCK" {
			return "BLOCK"
		}
		status = c.Status
	}
	return status
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
	if got := analyze(t, newFake(1), "MOVE", true).TargetConfig["uuid.action"]; got != "keep" {
		t.Fatalf("a MOVE target does not keep its identity: %q", got)
	}
	if got := analyze(t, newFake(1), "COPY", false).TargetConfig["uuid.action"]; got != "" {
		t.Fatalf("a COPY target was told it was moved: %q", got)
	}
}
