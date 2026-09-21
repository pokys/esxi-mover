package esxi

import (
	"context"
	"strings"
	"testing"
)

// The layout is what ESXi 6.5 printed for a snapshot taken by a live test,
// including VMware's own "Desciption" spelling.
const ownTree = "Get Snapshot:\n|-ROOT\n--Snapshot Name        : esxi-mover-0123abcd\n--Snapshot Id        : 1\n--Snapshot Desciption  : ESXi Mover live migration\n--Snapshot Created On  : 9/21/2026 14:56:33\n--Snapshot State       : powered off\n"

func TestSnapshotNamesReadsTheTree(t *testing.T) {
	names, e := SnapshotNames(ownTree)
	if e != nil || len(names) != 1 || names[0] != "esxi-mover-0123abcd" {
		t.Fatal(names, e)
	}
	if names, e := SnapshotNames("Get Snapshot:\n"); e != nil || len(names) != 0 {
		t.Fatal("an empty tree was not recognized", names, e)
	}
	for _, s := range []string{"", "error", ownTree + "----|-CHILD\n", ownTree + "unexpected line\n"} {
		if _, e := SnapshotNames(s); e == nil {
			t.Fatalf("unrecognized tree accepted: %q", s)
		}
	}
}

// The merge must never touch a snapshot someone else took, even alongside the
// job's own.
func TestConsolidateTouchesOnlyTheJobsSnapshot(t *testing.T) {
	foreign := "Get Snapshot:\n|-ROOT\n--Snapshot Name        : nightly-backup\n--Snapshot Id        : 1\n"
	both := ownTree + "----Snapshot Name        : nightly-backup\n"
	for _, tree := range []string{foreign, both, "Get Snapshot:\n"} {
		ex := &FakeExecutor{RunFunc: func(_ context.Context, c Command) (Result, error) {
			return Result{Stdout: tree}, nil
		}}
		if e := NewClient(ex).ConsolidateOwnSnapshot(context.Background(), 7, "esxi-mover-0123abcd"); e == nil {
			t.Fatalf("merged a tree that is not only the job's snapshot: %q", tree)
		}
		for _, c := range ex.Commands {
			if strings.Contains(c.Script, "removeall") {
				t.Fatal("a merge command was sent")
			}
		}
	}
	ex := &FakeExecutor{RunFunc: func(_ context.Context, c Command) (Result, error) {
		return Result{Stdout: ownTree}, nil
	}}
	if e := NewClient(ex).ConsolidateOwnSnapshot(context.Background(), 7, "esxi-mover-0123abcd"); e != nil {
		t.Fatal(e)
	}
	if last := ex.Commands[len(ex.Commands)-1]; last.Category != "consolidate-own-snapshot" {
		t.Fatal("the job's own snapshot was not merged")
	}
}

// A live clone copies the base disk of a running VM, so its worker must not
// insist on the VM being powered off.
func TestLiveCloneSkipsThePowerCheck(t *testing.T) {
	id := strings.Repeat("c", 32)
	ex := &FakeExecutor{RunFunc: func(_ context.Context, c Command) (Result, error) {
		switch c.Category {
		case "canonical-path":
			return Result{Stdout: "/vmfs/volumes/target/lab\n"}, nil
		case "read-file":
			return Result{Stdout: "job=" + id + "\n"}, nil
		}
		return Result{}, nil
	}}
	c := NewClient(ex)
	if e := c.StartClone(context.Background(), id, 0, 7, "/vmfs/volumes/source/lab/disk.vmdk", "/vmfs/volumes/target/lab/disk.vmdk", false); e != nil {
		t.Fatal(e)
	}
	script := ex.Commands[len(ex.Commands)-1].Script
	if !strings.Contains(script, "vmkfstools") || strings.Contains(script, "power.getstate") {
		t.Fatal("live clone script is wrong")
	}
}

func TestCopyToTargetNeverOverwrites(t *testing.T) {
	ex := &FakeExecutor{RunFunc: func(_ context.Context, c Command) (Result, error) {
		if c.Category == "canonical-path" {
			return Result{Stdout: "/vmfs/volumes/target/lab\n"}, nil
		}
		return Result{}, nil
	}}
	c := NewClient(ex)
	if e := c.CopyToTarget(context.Background(), "/vmfs/volumes/source/lab/lab-000001.vmdk", "/vmfs/volumes/target/lab"); e != nil {
		t.Fatal(e)
	}
	script := ex.Commands[len(ex.Commands)-1].Script
	if !strings.Contains(script, "'!' '-e'") || !strings.Contains(script, "'cp'") {
		t.Fatalf("copy may overwrite: %s", script)
	}
	if e := c.CopyToTarget(context.Background(), "/vmfs/volumes/source/lab/lab.vmx", "/vmfs/volumes/target/lab"); e == nil {
		t.Fatal("a configuration file was copied as a snapshot file")
	}
}
