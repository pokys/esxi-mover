package esxi

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const RuntimeDir = "/tmp/esxi-mover"
const activeDir = RuntimeDir + "/active"

var jobIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type CloneStatus struct {
	Done, Alive        bool
	ExitCode, Progress int
	Log                string
}

var progressPattern = regexp.MustCompile(`(?i)Clone:\s*(\d{1,3})(?:\.\d+)?%\s*done`)

func Progress(s string) int {
	p := 0
	for _, m := range progressPattern.FindAllStringSubmatch(s, -1) {
		n, _ := strconv.Atoi(m[1])
		if n <= 100 {
			p = n
		}
	}
	return p
}
func (c *Client) Existing(ctx context.Context) (string, error) {
	r, e := c.run(ctx, "existing-operation", "if "+Argv("test", "-e", activeDir)+"; then printf 'Existing ESXi Mover operation detected.\n'; "+Argv("find", activeDir, "-maxdepth", "1", "-type", "f", "-print")+"; for f in "+Quote(activeDir)+"/*.meta "+Quote(activeDir)+"/*.exit; do if [ -f \"$f\" ]; then head -c 4096 \"$f\"; printf '\n'; fi; done; fi", nil)
	return c.Redact(r.Stdout), e
}
func (c *Client) Acquire(ctx context.Context, id, meta string) error {
	if !jobIDPattern.MatchString(id) {
		return fmt.Errorf("invalid operation ID")
	}
	script := "umask 077; " + Argv("mkdir", "-p", RuntimeDir) + " && " + Argv("test", "!", "-L", RuntimeDir) + " && " + Argv("mkdir", activeDir) + " && " + Argv("sh", "-c", "cat > "+Quote(activeDir+"/job.meta"))
	_, e := c.run(ctx, "acquire-operation-lock", script, []byte("job="+id+"\n"+meta))
	return e
}
func (c *Client) Finish(ctx context.Context, id string) error {
	if !jobIDPattern.MatchString(id) {
		return fmt.Errorf("invalid operation ID")
	}
	meta, e := c.ReadFile(ctx, activeDir+"/job.meta")
	if e != nil || !strings.HasPrefix(meta, "job="+id+"\n") {
		return fmt.Errorf("operation lock ownership changed")
	}
	// Completed metadata is archived, never deleted. Failed/uncertain jobs retain active.
	_, e = c.run(ctx, "archive-completed-operation", Argv("mv", activeDir, RuntimeDir+"/completed-"+id), nil)
	return e
}
// TargetPath accepts a directory this tool may create and write into: a direct
// child of a datastore volume, never the volume root and never a nested path.
// The folder is named after the source VM, so its name carries no marker; that
// the job owns the directory is guaranteed by CreateTarget's mkdir, which fails
// when it already exists.
func TargetPath(p string) bool {
	if ValidPath(p) != nil || !strings.HasPrefix(p, "/vmfs/volumes/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(p, "/vmfs/volumes/"), "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.HasPrefix(parts[1], ".")
}
func (c *Client) CreateTarget(ctx context.Context, dir string) error {
	if !TargetPath(dir) {
		return fmt.Errorf("invalid target directory")
	}
	canonical, e := c.Canonical(ctx, path.Dir(dir))
	if e != nil || canonical != path.Dir(dir) {
		return fmt.Errorf("target parent changed or is not canonical")
	}
	_, e = c.command(ctx, "create-target", "mkdir", dir)
	return e
}
func (c *Client) checkTarget(ctx context.Context, dir string) error {
	if !TargetPath(dir) {
		return fmt.Errorf("invalid target directory")
	}
	canonical, e := c.Canonical(ctx, dir)
	if e != nil || canonical != dir {
		return fmt.Errorf("target directory changed")
	}
	return nil
}
func (c *Client) WriteTarget(ctx context.Context, dir, name string, data []byte) error {
	if e := c.checkTarget(ctx, dir); e != nil {
		return e
	}
	if name != path.Base(name) || ValidPath(name) != nil {
		return fmt.Errorf("invalid target config name")
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".vmx", ".nvram", ".vmxf":
	default:
		return fmt.Errorf("only configuration files may be written")
	}
	// Noclobber prevents accidental overwrite; disk data is never written here.
	_, e := c.run(ctx, "write-target-config", "umask 077; set -C; cat > "+Quote(path.Join(dir, name)), data)
	return e
}
// StartClone launches one detached vmkfstools clone. With requireOff the worker
// checks on ESXi, immediately before cloning, that the source is powered off.
// A live migration clones the base disk behind the job's own snapshot while
// the VM runs, so it passes false.
func (c *Client) StartClone(ctx context.Context, id string, index, vmID int, source, target string, requireOff bool) error {
	if !jobIDPattern.MatchString(id) || index < 0 || vmID <= 0 || path.Ext(source) != ".vmdk" || path.Ext(target) != ".vmdk" {
		return fmt.Errorf("invalid clone request")
	}
	if e := c.checkTarget(ctx, path.Dir(target)); e != nil {
		return e
	}
	meta, e := c.ReadFile(ctx, activeDir+"/job.meta")
	if e != nil || !strings.HasPrefix(meta, "job="+id+"\n") {
		return fmt.Errorf("operation lock ownership changed")
	}
	prefix := activeDir + "/disk-" + strconv.Itoa(index)
	clone := Argv("vmkfstools", "-i", source, target, "-d", "thin") + "\nrc=$?\n"
	if requireOff {
		clone = "state=$(" + Argv("vim-cmd", "vmsvc/power.getstate", strconv.Itoa(vmID)) + ")\nrc=$?\n" +
			"if [ \"$rc\" -eq 0 ]; then\ncase \"$state\" in\n'Powered off'|'Retrieved runtime info\nPowered off')\n" +
			clone + ";;\n*) rc=92 ;;\nesac\nfi\n"
	}
	body := "umask 077\nprintf '%s\\n' \"$$\" > " + Quote(prefix+".pid") + "\n" + clone +
		"printf '%s\\n' \"$rc\" > " + Quote(prefix+".exit.tmp") + " && " + Argv("mv", prefix+".exit.tmp", prefix+".exit") + "\n"
	script := "umask 077; " + Argv("mkdir", prefix+".started") + " || exit 90\n" +
		Argv("nohup", "sh", "-c", body) + " > " + Quote(prefix+".log") + " 2>&1 < /dev/null &\n"
	_, e = c.run(ctx, "start-detached-clone", script, nil)
	return e
}
func (c *Client) CloneStatus(ctx context.Context, index int) (CloneStatus, error) {
	var status CloneStatus
	prefix := activeDir + "/disk-" + strconv.Itoa(index)
	script := "if [ -f " + Quote(prefix+".exit") + " ]; then printf 'DONE '; cat " + Quote(prefix+".exit") + "; elif [ -f " + Quote(prefix+".pid") + " ]; then p=$(cat " + Quote(prefix+".pid") + "); case \"$p\" in ''|*[!0-9]*) printf 'UNKNOWN\\n';; *) if kill -0 \"$p\" 2>/dev/null; then printf 'RUNNING\\n'; else printf 'LOST\\n'; fi;; esac; else printf 'STARTING\\n'; fi\nif [ -f " + Quote(prefix+".log") + " ]; then tail -c 8192 " + Quote(prefix+".log") + "; fi"
	r, e := c.run(ctx, "poll-detached-clone", script, nil)
	if e != nil {
		return status, e
	}
	line, log, _ := strings.Cut(r.Stdout, "\n")
	status.Log = c.Redact(log)
	status.Progress = Progress(log)
	if strings.HasPrefix(line, "DONE ") {
		n, e := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "DONE ")))
		if e != nil || n < 0 || n > 255 {
			return status, fmt.Errorf("invalid detached exit code")
		}
		status.Done = true
		status.ExitCode = n
		return status, nil
	}
	switch line {
	case "RUNNING", "STARTING":
		status.Alive = true
		return status, nil
	default:
		return status, fmt.Errorf("detached process status is unknown; manual inspection required")
	}
}
