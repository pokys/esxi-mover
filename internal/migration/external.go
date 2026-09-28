package migration

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"esxi-mover/internal/esxi"
	"esxi-mover/internal/vmx"
)

// Disks outside the VM folder (experimental, Request.BringDisks). Such a disk
// is cloned into the target folder like any other; the original is only read.
// What needs care is everything around it: the folder it lies in may belong to
// another VM, may hold snapshot files of that disk, and its name may collide
// with a file already headed for the target folder.

// externalFile resolves a disk outside the VM folder. It must be an existing
// file on a mounted VMFS datastore this tool supports.
func (a Analyzer) externalFile(ctx context.Context, ref, dir string, ds []esxi.Datastore) (string, error) {
	p, e := esxi.ResolveReference(ref, dir, ds)
	if e != nil {
		return "", e
	}
	canonical, e := a.Host.Canonical(ctx, p)
	if e != nil {
		return "", e
	}
	supported := false
	for _, d := range ds {
		if d.Mounted && esxi.SupportedVMFS(d.Type) && strings.HasPrefix(canonical, path.Join("/vmfs/volumes", d.UUID)+"/") {
			supported = true
		}
	}
	if !supported {
		return "", fmt.Errorf("the disk is not on a mounted VMFS-5 or VMFS-6 datastore")
	}
	exists, e := a.Host.Exists(ctx, canonical)
	if e != nil || !exists {
		return "", fmt.Errorf("required file is missing or inaccessible")
	}
	return canonical, nil
}

// freeName is the name a disk from outside the VM folder gets in the target
// folder: its own name when nothing else uses it, otherwise name_1, name_2...
// A name counts as used when any file in the source folder has it, so a disk
// from elsewhere never takes the name of one that lives next to the VMX, or
// when a disk already planned for the target has it. VMFS names are compared
// without case, and the -flat extent vmkfstools writes is reserved too.
func freeName(name string, used ...map[string]bool) string {
	taken := func(n string) bool {
		for _, m := range used {
			for k := range m {
				if strings.EqualFold(k, n) {
					return true
				}
			}
		}
		return false
	}
	stem := strings.TrimSuffix(name, path.Ext(name))
	candidate := stem + ".vmdk"
	for i := 1; taken(candidate) || taken(strings.TrimSuffix(candidate, ".vmdk")+"-flat.vmdk"); i++ {
		candidate = stem + "_" + strconv.Itoa(i) + ".vmdk"
	}
	return candidate
}

// inspectFolder checks a folder outside the VM folder that holds some of its
// disks. Snapshot files of those disks and any VMX there that uses them block;
// anything else that shows the folder belongs to another VM is a warning.
func (a Analyzer) inspectFolder(ctx context.Context, r *Report, dir string, disks []string, ds []esxi.Datastore) error {
	files, e := a.Host.List(ctx, dir)
	if e != nil {
		return e
	}
	sort.Strings(files)
	ours := map[string]bool{}
	stems := []string{}
	for _, d := range disks {
		ours[d] = true
		stems = append(stems, strings.ToLower(strings.TrimSuffix(path.Base(d), ".vmdk"))+"-")
	}
	others := []string{}
	for _, f := range files {
		n := strings.ToLower(path.Base(f))
		if f == r.SourceVMX {
			continue
		}
		if SnapshotArtifact([]string{f}) != "" {
			belongs := false
			for _, s := range stems {
				if strings.HasPrefix(n, s) {
					belongs = true
				}
			}
			if belongs {
				detail, adv := artifactAdvice(r, dir, path.Base(f), files)
				r.advised("Disk folder", statusBlock, detail+" in "+dir, adv)
			} else {
				others = append(others, path.Base(f))
			}
			continue
		}
		if strings.HasSuffix(n, ".vmx") {
			uses, e := a.vmxUses(ctx, f, ours, ds)
			if e != nil || uses {
				why := "Another VMX in that folder names this disk. If that VM is registered and started later, both would write to the same disk."
				if e != nil {
					why = "Another VMX in that folder cannot be read or names a disk that cannot be resolved, so this tool cannot rule out that it uses this disk."
				}
				r.advised("Disk folder", statusBlock, path.Base(f)+" in "+dir+" may use a disk of this VM", advise("Another VM may use this disk", why,
					command("See which disks that VMX names.", "grep -i -E '\\.filename' "+esxi.Quote(f)),
					say("If that VM still needs the disk, the two VMs share it and cannot be moved one at a time. If it does not, remove the disk from that VM's settings."),
					again()))
				continue
			}
			others = append(others, path.Base(f))
		}
	}
	if len(others) > 0 {
		r.advised("Disk folder", statusWarning, dir+" also holds "+strings.Join(others, ", "), advise("A disk shares its folder with other files",
			"The folder looks like it belongs to another VM. Nothing there names this disk, so the copy is safe, but that VM's own files stay where they are.",
			say("Check that no other VM is expected to use the disk.")))
	}
	return nil
}

// vmxUses reports whether a VMX, registered or not, names one of the disks.
func (a Analyzer) vmxUses(ctx context.Context, file string, disks map[string]bool, ds []esxi.Datastore) (bool, error) {
	raw, e := a.Host.ReadFile(ctx, file)
	if e != nil {
		return false, e
	}
	cfg, e := vmx.Parse(raw)
	if e != nil {
		return false, e
	}
	for k, v := range cfg {
		if !strings.HasSuffix(k, ".filename") || !strings.HasSuffix(strings.ToLower(v), ".vmdk") {
			continue
		}
		// A reference that cannot be resolved could still be one of the
		// disks, so it counts as unknown rather than as another disk.
		p, e := esxi.ResolveReference(v, path.Dir(file), ds)
		if e != nil {
			return false, e
		}
		c, e := a.Host.Canonical(ctx, p)
		if e != nil {
			return false, e
		}
		if disks[c] {
			return true, nil
		}
	}
	return false, nil
}
