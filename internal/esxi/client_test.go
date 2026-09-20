package esxi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type FakeExecutor struct {
	RunFunc  func(context.Context, Command) (Result, error)
	Commands []Command
}

func (f *FakeExecutor) Run(ctx context.Context, c Command) (Result, error) {
	f.Commands = append(f.Commands, c)
	if f.RunFunc != nil {
		return f.RunFunc(ctx, c)
	}
	return Result{}, nil
}
func TestVersionFixtures(t *testing.T) {
	for _, v := range []string{"6.5", "6.7", "7.0", "8.0"} {
		t.Run(v, func(t *testing.T) {
			read := func(n string) string {
				b, e := os.ReadFile(filepath.Join("../../fixtures/esxi-"+v, n))
				if e != nil {
					t.Fatal(e)
				}
				return string(b)
			}
			if !versionPattern.MatchString(read("version.txt")) {
				t.Fatal("version not recognized")
			}
			vms, e := ParseVMs(read("getallvms.txt"))
			if e != nil || len(vms) != 2 || vms[0].Name != "Lab Server" || !strings.Contains(vms[1].VMXPath, "VM $box.vmx") {
				t.Fatalf("VM inventory: %v %+v", e, vms)
			}
			ds, e := ParseDatastores(read("filesystems.txt"))
			if e != nil || len(ds) != 2 || ds[1].Name != "Store B" || ds[0].Free != 824633720832 {
				t.Fatalf("datastores: %v %+v", e, ds)
			}
			for file, want := range map[string]Power{"power-on.txt": On, "power-off.txt": Off, "power-suspended.txt": Suspended} {
				p, e := ParsePower(read(file))
				if e != nil || p != want {
					t.Fatal(file, e)
				}
			}
			ex := &FakeExecutor{RunFunc: func(_ context.Context, c Command) (Result, error) { return Result{Stdout: read("verify.txt")}, nil }}
			if e := NewClient(ex).VerifyChain(context.Background(), "disk.vmdk"); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestUnknownResponses(t *testing.T) {
	if _, e := ParseVMs("Vmid Name File\nSkipping invalid VM 4\n"); e == nil {
		t.Fatal("invalid inventory accepted")
	}
	if _, e := ParsePower("Powered off\nPowered on"); e == nil {
		t.Fatal("ambiguous power accepted")
	}
	if _, e := ParseDatastores("unexpected format"); e == nil {
		t.Fatal("unknown format accepted")
	}
}
func TestAuthoritativeVMPathAndInventory(t *testing.T) {
	raw := "(vim.vm.ConfigInfo) {\n files = (vim.vm.FileInfo) {\n vmPathName = \"[source] VM's Server/VM.vmx\",\n },\n}\n"
	p, e := ParseVMPath(raw)
	if e != nil || p != "[source] VM's Server/VM.vmx" {
		t.Fatal(p, e)
	}
	for _, s := range []string{"unknown", raw + raw, `vmPathName = "/tmp/VM.vmx",`} {
		if _, e := ParseVMPath(s); e == nil {
			t.Fatal("ambiguous VM path accepted")
		}
	}
	fake := &FakeExecutor{RunFunc: func(_ context.Context, c Command) (Result, error) {
		if c.Category == "inventory" {
			return Result{Stdout: "Vmid  Name  File  Guest OS  Version\n7  VM's Server  [source] VM's Server/VM.vmx  otherGuest64  vmx-13\n"}, nil
		}
		return Result{Stdout: raw}, nil
	}}
	if v, e := NewClient(fake).VMs(context.Background()); e != nil || len(v) != 1 {
		t.Fatal("authoritative inventory", e)
	}
	raw = strings.ReplaceAll(raw, "VM's Server/VM.vmx", "elsewhere/VM.vmx")
	if _, e := NewClient(fake).VMs(context.Background()); e == nil {
		t.Fatal("inventory identity mismatch accepted")
	}
}
func TestUnnamedAndUnmountedDatastores(t *testing.T) {
	raw := "Mount Point  Volume Name  UUID  Mounted  Type  Size  Free\n/vmfs/volumes/boot-uuid      boot-uuid  true  vfat  100  50\n  offline-store  offline-uuid  false  VMFS-6  100  50\n"
	d, e := ParseDatastores(raw)
	if e != nil || len(d) != 2 || d[0].Name != "" || d[1].Mount != "" || d[1].Mounted {
		t.Fatal(d, e)
	}
}
func TestFailedConfigurationReadDoesNotLeakContents(t *testing.T) {
	fake := &FakeExecutor{RunFunc: func(context.Context, Command) (Result, error) {
		return Result{ExitCode: 1, Stdout: "private-configuration-content", Stderr: "secret-error-fragment"}, nil
	}}
	c := NewClient(fake)
	_, e := c.ReadFile(context.Background(), "fixture")
	if e == nil {
		t.Fatal("read should fail")
	}
	for _, event := range c.Audit.Events() {
		if strings.Contains(event.Error, "private") || strings.Contains(event.Error, "secret-error") {
			t.Fatal("configuration content leaked into audit")
		}
	}
}
func TestShellQuoting(t *testing.T) {
	shell, e := exec.LookPath("sh")
	if e != nil {
		t.Skip("POSIX sh unavailable")
	}
	for _, value := range []string{"normal", "VM Name", "VM's Server", "$(printf injected)", "; rm ignored", "&", "\"", "'", "`", "line\nnext", ""} {
		out, e := exec.Command(shell, "-c", Argv("printf", "%s", value)).Output()
		if e != nil || string(out) != value {
			t.Errorf("quoting %q => %q: %v", value, out, e)
		}
	}
}
func TestResolveReferences(t *testing.T) {
	ds := []Datastore{{Name: "Store A", UUID: "uuid-a"}}
	for _, ref := range []string{"disk.vmdk", "[Store A] VM/disk.vmdk", "/vmfs/volumes/uuid-a/VM/disk.vmdk"} {
		got, e := ResolveReference(ref, "/vmfs/volumes/uuid-a/VM", ds)
		if e != nil || got != "/vmfs/volumes/uuid-a/VM/disk.vmdk" {
			t.Fatal(ref, got, e)
		}
	}
	for _, ref := range []string{"../disk.vmdk", "/tmp/disk.vmdk", "a\nb.vmdk", "[unknown] a.vmdk"} {
		if _, e := ResolveReference(ref, "/vmfs/volumes/uuid-a/VM", ds); e == nil {
			t.Fatal("unsafe path accepted", ref)
		}
	}
}
func TestMovedQuestionDynamicIndex(t *testing.T) {
	id, choice, e := MovedAnswer("Virtual machine message 27:\nmsg.uuid.altered:This virtual machine may have been moved or copied.\n0. Cancel (Cancel)\n7. I _moved it (I _moved it)\n2. I _copied it (I _copied it) [default]\n")
	if e != nil || id != "27" || choice != "7" {
		t.Fatal(id, choice, e)
	}
	for _, s := range []string{"message 27\n1. I moved it", "Virtual machine message 1:\nother.question\n1. I moved it", "Virtual machine message 1:\nmsg.uuid.altered\n1. I moved it\n2. I moved it"} {
		if _, _, e := MovedAnswer(s); e == nil {
			t.Fatal("ambiguous answer accepted")
		}
	}
}
func TestDetachedBuilderAndPolling(t *testing.T) {
	id := strings.Repeat("a", 32)
	ex := &FakeExecutor{RunFunc: func(_ context.Context, c Command) (Result, error) {
		switch c.Category {
		case "canonical-path":
			return Result{Stdout: "/vmfs/volumes/target/esxi-mover-test\n"}, nil
		case "read-file":
			return Result{Stdout: "job=" + id + "\n"}, nil
		case "poll-detached-clone":
			return Result{Stdout: "DONE 0\nClone: 100% done.\n"}, nil
		}
		return Result{}, nil
	}}
	c := NewClient(ex)
	if e := c.StartClone(context.Background(), id, 0, 7, "/vmfs/volumes/source/VM's $(bad)/disk.vmdk", "/vmfs/volumes/target/esxi-mover-test/disk.vmdk"); e != nil {
		t.Fatal(e)
	}
	script := ex.Commands[len(ex.Commands)-1].Script
	for _, required := range []string{"nohup", "power.getstate", "Powered off", "vmkfstools", "thin", ".started", ".exit.tmp", ".pid", "/dev/null"} {
		if !strings.Contains(script, required) {
			t.Fatal("missing detached guard", required)
		}
	}
	if strings.Contains(script, "rm ") {
		t.Fatal("deletion in script")
	}
	status, e := c.CloneStatus(context.Background(), 0)
	if e != nil || !status.Done || status.ExitCode != 0 || status.Progress != 100 {
		t.Fatal(status, e)
	}
}
func TestReadLimitAndWriteRestriction(t *testing.T) {
	ex := &FakeExecutor{RunFunc: func(_ context.Context, c Command) (Result, error) {
		if c.Category == "read-file" {
			return Result{Stdout: strings.Repeat("x", (1<<20)+1)}, nil
		}
		return Result{Stdout: "/vmfs/volumes/t/esxi-mover-1\n"}, nil
	}}
	c := NewClient(ex)
	if _, e := c.ReadFile(context.Background(), "x"); e == nil {
		t.Fatal("oversized file accepted")
	}
	if e := c.WriteTarget(context.Background(), "/vmfs/volumes/t/esxi-mover-1", "disk.vmdk", []byte("bad")); e == nil {
		t.Fatal("VMDK generic write allowed")
	}
	if e := c.WriteTarget(context.Background(), "/vmfs/volumes/source/VM", "vm.vmx", nil); e == nil {
		t.Fatal("source configuration write allowed")
	}
}
func TestNonzeroAndUnknownVerification(t *testing.T) {
	for _, r := range []Result{{ExitCode: 1, Stderr: "failure"}, {Stdout: "everything maybe okay"}} {
		ex := &FakeExecutor{RunFunc: func(context.Context, Command) (Result, error) { return r, nil }}
		if e := NewClient(ex).VerifyChain(context.Background(), "x"); e == nil {
			t.Fatal("unknown verification accepted")
		}
	}
	if Progress("disk-name-99%\nClone: 10% done.\rClone: 55% done.\rClone: 999% done.\rClone: 78.4% done.") != 78 {
		t.Fatal("bad progress")
	}
}
func ExampleQuote() {
	fmt.Println(Quote("VM's Server")) // Output: 'VM'"'"'s Server'
}

// ESXi's shell answers 127 for every "command -v" probe because it has no such
// builtin, which reported the first required name as missing on a host that had
// all of them.
func TestCapabilityProbeDoesNotRelyOnTheCommandBuiltin(t *testing.T) {
	version, e := os.ReadFile("../../fixtures/esxi-6.5/version.txt")
	if e != nil {
		t.Fatal(e)
	}
	fake := &FakeExecutor{}
	fake.RunFunc = func(_ context.Context, c Command) (Result, error) {
		if strings.HasPrefix(c.Script, "command ") {
			return Result{Stderr: "sh: command: not found", ExitCode: 127}, nil
		}
		if c.Category == "version" {
			return Result{Stdout: string(version)}, nil
		}
		return Result{Stdout: "/bin/tool\n"}, nil
	}
	// Later inventory parsing fails on this stub output; only the probe matters.
	if _, e = NewClient(fake).Inventory(context.Background()); e != nil &&
		strings.Contains(e.Error(), "required ESXi command unavailable") {
		t.Fatal("capability probe depends on a builtin ESXi does not provide:", e)
	}
	probed := map[string]bool{}
	for _, c := range fake.Commands {
		if c.Category != "capability" {
			continue
		}
		if !strings.HasPrefix(c.Script, "which ") {
			t.Fatalf("capability probe is not portable to ESXi: %q", c.Script)
		}
		probed[strings.Trim(strings.TrimPrefix(c.Script, "which "), "'")] = true
	}
	for _, name := range []string{"vim-cmd", "vmkfstools", "esxcli", "test", "kill"} {
		if !probed[name] {
			t.Fatalf("required command %q was never probed", name)
		}
	}
}

// A real 6.5 host rejected its whole inventory because vim-cmd prints VM
// annotations inline: a VM with notes spans several lines, and every
// continuation line failed the row pattern.
func TestParseVMsAcceptsMultiLineAnnotations(t *testing.T) {
	out := "Vmid   Name        File                                    Guest OS       Version   Annotation\n" +
		"7      Lab Server  [datastore1] Lab Server/Lab Server.vmx    otherGuest64   vmx-13\n" +
		"12     Noted VM    [Store B] Noted VM/Noted VM.vmx           ubuntu64Guest  vmx-13    status checks:\n" +
		"second line of the note\n" +
		"\n" +
		"third line: https://example.invalid/page\n" +
		"21     Last VM     [Store B] Last VM/Last VM.vmx             debian9Guest   vmx-13\n"
	vms, e := ParseVMs(out)
	if e != nil {
		t.Fatal(e)
	}
	if len(vms) != 3 {
		t.Fatalf("expected 3 VMs, got %d: %+v", len(vms), vms)
	}
	if vms[2].ID != 21 || vms[2].Name != "Last VM" {
		t.Fatalf("the row following an annotation was lost: %+v", vms[2])
	}
}

// Only a row that actually carried notes may be continued; the parser must
// still refuse an inventory it does not understand.
func TestParseVMsStillRejectsUnknownRows(t *testing.T) {
	out := "Vmid   Name        File                                    Guest OS       Version   Annotation\n" +
		"7      Lab Server  [datastore1] Lab Server/Lab Server.vmx    otherGuest64   vmx-13\n" +
		"this row had no annotation, so this line is not a continuation\n"
	if _, e := ParseVMs(out); e == nil {
		t.Fatal("unrecognized inventory row accepted")
	}
}
