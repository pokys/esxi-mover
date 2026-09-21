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

// Run the exact generated worker and stop script in a real POSIX shell: the
// stop ends the recorded clone process, and the worker still publishes a
// non-zero exit code, which is how the engine learns the clone ended.
func TestStopEndsTheRecordedCloneAndTheWorkerReportsIt(t *testing.T) {
	shell, e := exec.LookPath("sh")
	if e != nil {
		t.Skip("POSIX shell unavailable")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	runtime := filepath.Join(dir, "active")
	for _, d := range []string{bin, runtime} {
		if e := os.MkdirAll(d, 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(bin, "vmkfstools"), []byte("#!/bin/sh\nprintf 'Clone: 10%% done.\\n'\nsleep 30\nexit 0\n"), 0700); e != nil {
		t.Fatal(e)
	}
	id := strings.Repeat("d", 32)
	fake := &FakeExecutor{RunFunc: func(_ context.Context, c Command) (Result, error) {
		switch c.Category {
		case "canonical-path":
			return Result{Stdout: "/vmfs/volumes/target/lab\n"}, nil
		case "read-file":
			return Result{Stdout: "job=" + id + "\n"}, nil
		}
		return Result{}, nil
	}}
	client := NewClient(fake)
	local := func(script string) *exec.Cmd {
		script = strings.ReplaceAll(script, activeDir, filepath.ToSlash(runtime))
		script = "fixture_bin=$(cd " + Quote(filepath.ToSlash(bin)) + " && pwd)\nPATH=\"$fixture_bin:$PATH\"\nexport PATH\n" + script
		cmd := exec.Command(shell, "-c", script)
		cmd.Dir = dir
		return cmd
	}
	if e := client.StartClone(context.Background(), id, 0, 7, "/vmfs/volumes/source/lab/disk.vmdk", "/vmfs/volumes/target/lab/disk.vmdk", false); e != nil {
		t.Fatal(e)
	}
	if out, e := local(fake.Commands[len(fake.Commands)-1].Script).CombinedOutput(); e != nil {
		t.Fatalf("launcher: %s %v", out, e)
	}
	waitFor := func(name string) []byte {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if b, e := os.ReadFile(filepath.Join(runtime, name)); e == nil && len(b) > 0 {
				return b
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s never appeared", name)
		return nil
	}
	waitFor("disk-0.clonepid")
	if e := client.StopClone(context.Background(), 0); e != nil {
		t.Fatal(e)
	}
	stop := fake.Commands[len(fake.Commands)-1]
	if stop.Category != "stop-clone" || !strings.Contains(stop.Script, "kill") || !strings.Contains(stop.Script, ".clonepid") {
		t.Fatalf("unexpected stop script: %s", stop.Script)
	}
	if out, e := local(stop.Script).CombinedOutput(); e != nil {
		t.Fatalf("stop: %s %v", out, e)
	}
	code := strings.TrimSpace(string(waitFor("disk-0.exit")))
	if code == "0" || code == "" {
		t.Fatalf("a stopped clone reported success: %q", code)
	}
	// Once the clone has ended, a second stop does nothing and succeeds.
	if out, e := local(stop.Script).CombinedOutput(); e != nil {
		t.Fatalf("stop after the end: %s %v", out, e)
	}
}
