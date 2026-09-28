package migration

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"esxi-mover/internal/esxi"
)

// fakeHost stands in for an ESXi host: files, sizes and registrations live in
// maps, and every mutation is recorded as an event the tests can order.
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
	targetSnapshot                                                                                                     string
	locked                                                                                                             bool
	inventoryCalls, snapshotAt                                                                                         int
	cloneFail, verifyFail                                                                                              int
	clones                                                                                                             int
	registerFail, registerLost, unregisterLost, shutdownStuck, powerFail, corruptConfig, launchLost, allocationUnknown bool
	question                                                                                                           string
	lastAnswer                                                                                                         string
	pollErrors                                                                                                         int
	snapshotFail, foreignSnapshot, cloneHangs, cloneStopped                                                            bool
	noTools, toolsUnknown                                                                                              bool
	autostart                                                                                                          map[int]esxi.AutoStart
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

// Snapshot reports the tree of the VM's own files: the source, or a target
// registered with the snapshot metadata copied from the source.
func (h *fakeHost) Snapshot(_ context.Context, id int) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if strings.Contains(h.vmxOf(id), "/target/") {
		if h.targetSnapshot == "" {
			return "Get Snapshot:\n", nil
		}
		return h.targetSnapshot, nil
	}
	return h.snapshot, nil
}
func (h *fakeHost) Autostart(context.Context) (map[int]esxi.AutoStart, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.autostart, nil
}
func (h *fakeHost) ToolsRunning(context.Context, int) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.toolsUnknown {
		return false, fmt.Errorf("unknown VMware Tools status")
	}
	return !h.noTools, nil
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
	if strings.HasSuffix(name, ".vmsd") {
		// A VM registered from the written metadata sees the same tree.
		h.targetSnapshot = h.snapshot
	}
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
	// As observed on ESXi 6.5: a disk named by its full path gets its delta
	// next to it, the VMX names the delta by full path, the delta names its
	// parent by file name, and the VMSD keeps the parent's full path.
	for _, line := range strings.Split(h.files[p], "\n") {
		k, v, ok := strings.Cut(line, " = ")
		v = strings.Trim(v, "\"")
		if !ok || !strings.HasSuffix(strings.ToLower(k), ".filename") || !strings.HasPrefix(v, "/vmfs/volumes/") || !strings.HasSuffix(v, ".vmdk") {
			continue
		}
		stem := strings.TrimSuffix(v, ".vmdk")
		cfg = strings.Replace(cfg, "\""+v+"\"", "\""+stem+"-000001.vmdk\"", 1)
		h.files[stem+"-000001.vmdk"] = fmt.Sprintf("version=1\nCID=5678abcd\nparentCID=1234abcd\ncreateType=\"seSparse\"\nparentFileNameHint=\"%s\"\nRW 2097152 SESPARSE \"%s-000001-sesparse.vmdk\"\n", path.Base(v), path.Base(stem))
		h.sizes[stem+"-000001-sesparse.vmdk"] = 4 << 20
		vmsd += fmt.Sprintf("snapshot0.disk%d.fileName = \"%s\"\n", n, v)
		n++
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
	p := h.vmxOf(id)
	target := strings.Contains(p, "/target/")
	tree := h.snapshot
	if target {
		tree = h.targetSnapshot
	}
	if !strings.Contains(tree, ": "+name+"\n") {
		return fmt.Errorf("not the job's snapshot")
	}
	dir := path.Dir(p)
	h.files[p] = strings.ReplaceAll(h.files[p], "-000001.vmdk\"", ".vmdk\"")
	// A source merge also removes deltas next to disks in other folders.
	ours := func(k string) bool {
		if target {
			return path.Dir(k) == dir
		}
		return !strings.HasPrefix(k, "/vmfs/volumes/target/")
	}
	for k := range h.files {
		if ours(k) && strings.Contains(k, "-000001") {
			delete(h.files, k)
		}
	}
	for k := range h.sizes {
		if (ours(k) && strings.Contains(k, "-000001")) || (path.Dir(k) == dir && strings.HasSuffix(k, ".vmsn")) {
			delete(h.sizes, k)
		}
	}
	h.files[dir+"/lab.vmsd"] = ".encoding = \"UTF-8\"\n"
	if target {
		h.targetSnapshot = "Get Snapshot:\n"
	} else {
		h.snapshot = "Get Snapshot:\n"
	}
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
	if strings.HasSuffix(dst, ".vmsd") {
		// A VM registered from the copied metadata sees the same tree.
		h.targetSnapshot = h.snapshot
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

// requireAdvice fails when a blocking check leaves the operator without a
// title and a next step, or when a command still holds a placeholder.
func requireAdvice(t *testing.T, r Report) {
	t.Helper()
	for _, c := range r.Checks {
		if c.Status != "BLOCK" {
			continue
		}
		if c.Title == "" || c.Why == "" || len(c.Steps) == 0 {
			t.Errorf("%s blocks without advice: %q", c.Name, c.Detail)
		}
		for _, s := range c.Steps {
			if strings.ContainsAny(s.Command, "<>") && !strings.Contains(s.Command, "2>/dev/null") {
				t.Errorf("%s has a placeholder command: %q", c.Name, s.Command)
			}
		}
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
func find(r Report, name string) []Check {
	out := []Check{}
	for _, c := range r.Checks {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}
