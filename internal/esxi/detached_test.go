package esxi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Execute the exact generated POSIX worker with fake ESXi binaries. The initiating
// shell exits before the worker, which must still publish its completion marker.
func TestDetachedWorkerSurvivesInitiatingShell(t *testing.T) {
	shell, e := exec.LookPath("sh")
	if e != nil {
		t.Skip("POSIX shell unavailable")
	}
	for _, power := range []string{"Powered off", "Powered on"} {
		t.Run(power, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			runtime := filepath.Join(dir, "active")
			if e := os.MkdirAll(bin, 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.Mkdir(runtime, 0700); e != nil {
				t.Fatal(e)
			}
			scripts := map[string]string{"vim-cmd": "#!/bin/sh\nprintf '%s\\n' 'Retrieved runtime info' '" + power + "'\n", "vmkfstools": "#!/bin/sh\nprintf 'Clone: 25%% done.\\n'\nsleep 0.1\nprintf 'Clone: 100%% done.\\n'\nexit 0\n"}
			for name, body := range scripts {
				if e := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); e != nil {
					t.Fatal(e)
				}
			}
			id := strings.Repeat("b", 32)
			fake := &FakeExecutor{RunFunc: func(_ context.Context, c Command) (Result, error) {
				switch c.Category {
				case "canonical-path":
					return Result{Stdout: "/vmfs/volumes/target/esxi-mover-test\n"}, nil
				case "read-file":
					return Result{Stdout: "job=" + id + "\n"}, nil
				}
				return Result{}, nil
			}}
			client := NewClient(fake)
			if e := client.StartClone(context.Background(), id, 0, 7, "/vmfs/volumes/source/VM's $(touch SHOULD_NOT_EXIST)/disk.vmdk", "/vmfs/volumes/target/esxi-mover-test/disk.vmdk"); e != nil {
				t.Fatal(e)
			}
			script := strings.ReplaceAll(fake.Commands[len(fake.Commands)-1].Script, activeDir, filepath.ToSlash(runtime))
			script = "fixture_bin=$(cd " + Quote(filepath.ToSlash(bin)) + " && pwd)\nPATH=\"$fixture_bin:$PATH\"\nexport PATH\n" + script
			cmd := exec.Command(shell, "-c", script)
			cmd.Dir = dir
			if output, e := cmd.CombinedOutput(); e != nil {
				t.Fatalf("launcher: %s %v", output, e)
			}
			deadline := time.Now().Add(3 * time.Second)
			var result []byte
			for time.Now().Before(deadline) {
				result, e = os.ReadFile(filepath.Join(runtime, "disk-0.exit"))
				if e == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if e != nil {
				t.Fatal("worker did not publish exit marker", e)
			}
			expected := "0"
			if power == "Powered on" {
				expected = "92"
			}
			if strings.TrimSpace(string(result)) != expected {
				t.Fatalf("exit %s, want %s", result, expected)
			}
			log, e := os.ReadFile(filepath.Join(runtime, "disk-0.log"))
			if e != nil {
				t.Fatal(e)
			}
			if power == "Powered off" && !strings.Contains(string(log), "100%") {
				t.Fatal("detached clone did not finish")
			}
			if power == "Powered on" && strings.Contains(string(log), "Clone") {
				t.Fatal("clone ran while powered on")
			}
			if _, e = os.Stat(filepath.Join(dir, "SHOULD_NOT_EXIST")); !os.IsNotExist(e) {
				t.Fatal("shell injection")
			}
		})
	}
}

// The folder is named after the source VM now, so the old "esxi-mover-" marker
// is gone; what still has to hold is where the directory sits.
func TestTargetPathAcceptsAnyDirectChildOfAVolume(t *testing.T) {
	for _, ok := range []string{
		"/vmfs/volumes/5a1b2c3d-00112233/Lab & Test",
		"/vmfs/volumes/target/esxi-mover-test",
		"/vmfs/volumes/t/a b c",
	} {
		if !TargetPath(ok) {
			t.Fatalf("a valid target directory was rejected: %q", ok)
		}
	}
	for _, bad := range []string{
		"/vmfs/volumes/5a1b2c3d",           // the volume root itself
		"/vmfs/volumes/5a1b2c3d/a/b",       // nested below the volume
		"/vmfs/volumes/5a1b2c3d/.hidden",   // hidden directory
		"/tmp/somewhere",                   // outside the datastores
		"/vmfs/volumes/5a1b2c3d/../escape", // traversal
		"/vmfs/volumes//empty",
	} {
		if TargetPath(bad) {
			t.Fatalf("an unsafe target directory was accepted: %q", bad)
		}
	}
}
