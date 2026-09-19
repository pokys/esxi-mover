package migration

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"esxi-mover/internal/vmdk"
	"esxi-mover/internal/vmx"
)

var snapshotName = regexp.MustCompile(`(?i)-[0-9]{6,}\.vmdk$`)

func SnapshotManager(s string) error {
	lines := []string{}
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			lines = append(lines, t)
		}
	}
	// ESXi commonly prints only its header when the snapshot tree is empty.
	if len(lines) == 1 && lines[0] == "Get Snapshot:" {
		return nil
	}
	if len(lines) == 2 && lines[0] == "Get Snapshot:" && lines[1] == "No snapshots." {
		return nil
	}
	return fmt.Errorf("snapshot manager reports a snapshot or an unknown response")
}
func DiskSnapshot(file string, d vmdk.Descriptor) error {
	if snapshotName.MatchString(file) {
		return fmt.Errorf("active VMX references a numbered snapshot VMDK")
	}
	if !strings.EqualFold(d.ParentCID, "ffffffff") || d.ParentHint != "" {
		return fmt.Errorf("active VMDK has a parent chain")
	}
	lower := strings.ToLower(d.CreateType)
	if strings.Contains(lower, "sparse") {
		return fmt.Errorf("snapshot/sparse createType")
	}
	for _, e := range d.Extents {
		n := strings.ToLower(e.File)
		if strings.HasSuffix(n, "-delta.vmdk") || strings.HasSuffix(n, "-sesparse.vmdk") {
			return fmt.Errorf("active snapshot extent")
		}
	}
	return nil
}
func SnapshotFiles(files []string) error {
	for _, f := range files {
		n := strings.ToLower(path.Base(f))
		if strings.HasSuffix(n, "-delta.vmdk") || strings.HasSuffix(n, "-sesparse.vmdk") || snapshotName.MatchString(n) || strings.HasSuffix(n, ".vmsn") || strings.HasSuffix(n, ".vmss") {
			return fmt.Errorf("snapshot/suspend artifact %s needs manual review; orphan status cannot be proven", path.Base(f))
		}
	}
	return nil
}
func VMSD(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	c, e := vmx.Parse(s)
	if e != nil {
		return fmt.Errorf("unreadable snapshot metadata")
	}
	// Only explicitly empty metadata is accepted. Even stale entries block V1.
	if c["snapshot.numsnapshots"] != "0" {
		return fmt.Errorf("VMSD snapshot metadata is active or ambiguous")
	}
	for k, v := range c {
		if k == "snapshot.numsnapshots" || k == ".encoding" || k == "snapshot.lastuid" {
			continue
		}
		if k == "snapshot.current" && (v == "0" || v == "-1") {
			continue
		}
		return fmt.Errorf("stale or ambiguous VMSD entry requires manual review: %s", k)
	}
	return nil
}
