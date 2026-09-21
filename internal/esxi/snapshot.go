package esxi

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A live migration takes one snapshot of its own and merges it again. This
// file holds the tool's only snapshot operations, and the merge refuses to run
// unless the VM's snapshot tree is exactly the one snapshot the job created,
// so a snapshot made by anyone else is never touched.

var ownSnapshot = regexp.MustCompile(`^esxi-mover-[a-f0-9]{8}$`)
var treeName = regexp.MustCompile(`^-+Snapshot Name\s*:\s*(.*)$`)
var treeField = regexp.MustCompile(`^-+Snapshot (Id|Desciption|Description|Created On|State)\s*:`)

// SnapshotNames reads the names out of vim-cmd vmsvc/snapshot.get. Any line it
// does not recognize fails the parse, so an unexpected tree is never mistaken
// for a known one.
func SnapshotNames(s string) ([]string, error) {
	names := []string{}
	header := false
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "" || t == "No snapshots.":
		case t == "Get Snapshot:":
			header = true
		case t == "|-ROOT":
		case treeName.MatchString(t):
			names = append(names, strings.TrimSpace(treeName.FindStringSubmatch(t)[1]))
		case treeField.MatchString(t):
		default:
			return nil, fmt.Errorf("unrecognized snapshot tree")
		}
	}
	if !header {
		return nil, fmt.Errorf("unrecognized snapshot tree")
	}
	return names, nil
}

// CreateSnapshot takes a snapshot without memory and without quiescing, the
// same kind a backup takes. The base disks become read-only and can be cloned
// while the VM keeps running.
func (c *Client) CreateSnapshot(ctx context.Context, id int, name string) error {
	if !ownSnapshot.MatchString(name) {
		return fmt.Errorf("invalid snapshot name")
	}
	_, e := c.runFor(ctx, 10*time.Minute, "create-snapshot", Argv("vim-cmd", "vmsvc/snapshot.create", strconv.Itoa(id), name, "ESXi Mover live migration", "0", "0"), nil)
	return e
}

// ConsolidateOwnSnapshot merges the job's snapshot back into the base disks.
// It runs only when that snapshot is the VM's one and only snapshot.
func (c *Client) ConsolidateOwnSnapshot(ctx context.Context, id int, name string) error {
	if !ownSnapshot.MatchString(name) {
		return fmt.Errorf("invalid snapshot name")
	}
	tree, e := c.Snapshot(ctx, id)
	if e != nil {
		return e
	}
	names, e := SnapshotNames(tree)
	if e != nil {
		return e
	}
	if len(names) != 1 || names[0] != name {
		return fmt.Errorf("the snapshot tree is not exactly this job's snapshot; nothing was merged")
	}
	_, e = c.runFor(ctx, 4*time.Hour, "consolidate-own-snapshot", Argv("vim-cmd", "vmsvc/snapshot.removeall", strconv.Itoa(id)), nil)
	return e
}

// CopyToTarget copies a snapshot delta or its metadata into the job's target
// folder. It never overwrites a file.
func (c *Client) CopyToTarget(ctx context.Context, source, dir string) error {
	if e := c.checkTarget(ctx, dir); e != nil {
		return e
	}
	name := path.Base(source)
	if ValidPath(source) != nil || !strings.HasPrefix(source, "/vmfs/volumes/") || ValidPath(name) != nil {
		return fmt.Errorf("invalid snapshot file")
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".vmdk", ".vmsd", ".vmsn":
	default:
		return fmt.Errorf("only snapshot files may be copied")
	}
	target := path.Join(dir, name)
	_, e := c.runFor(ctx, 4*time.Hour, "copy-snapshot-file", Argv("test", "!", "-e", target)+" && "+Argv("cp", source, target), nil)
	return e
}
