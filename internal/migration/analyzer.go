package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"

	"esxi-mover/internal/esxi"
	"esxi-mover/internal/vmdk"
	"esxi-mover/internal/vmx"
)

type Analyzer struct {
	Host          Host
	ApplianceUUID string
}

func (a Analyzer) Analyze(ctx context.Context, req Request) (Report, error) {
	return a.inspect(ctx, req, NewID(), false, false, 0)
}
func reserve(bytes int64) (int64, error) {
	if bytes < 0 || bytes > (math.MaxInt64-(1<<30))/115*100 {
		return 0, fmt.Errorf("disk capacity overflow")
	}
	return bytes + bytes/100*15 + (1 << 30), nil
}
func (a Analyzer) inspect(ctx context.Context, req Request, id string, ownLock, targetCreated bool, consumed int64) (Report, error) {
	r := Report{ID: id, Request: req, Ready: true}
	if e := validateRequest(req); e != nil {
		return r, e
	}
	inv, e := a.Host.Inventory(ctx)
	if e != nil {
		return r, e
	}
	if !inv.Capabilities.Supported {
		r.check("ESXi version", "BLOCK", "Only recognized ESXi 6.5, 6.7, 7.x and 8.x versions are supported")
	}
	if inv.ExistingOperation != "" && !ownLock {
		r.check("Existing operation", "BLOCK", inv.ExistingOperation)
	}
	found := false
	for _, v := range inv.VMs {
		if v.ID == req.VMID {
			r.VM = v
			found = true
		}
	}
	if !found {
		return r, fmt.Errorf("selected VM no longer exists")
	}
	var target esxi.Datastore
	for _, d := range inv.Datastores {
		if d.UUID == req.TargetUUID {
			target = d
		}
	}
	if !target.Mounted || !esxi.SupportedVMFS(target.Type) {
		r.check("Target datastore", "BLOCK", "Target must be a mounted VMFS-5 or VMFS-6 datastore")
		return r, nil
	}
	p, e := esxi.ResolveReference(r.VM.VMXPath, "", inv.Datastores)
	if e != nil {
		return r, e
	}
	r.SourceVMX, e = a.Host.Canonical(ctx, p)
	if e != nil {
		return r, e
	}
	r.SourceDir = path.Dir(r.SourceVMX)
	var source esxi.Datastore
	for _, d := range inv.Datastores {
		if strings.HasPrefix(r.SourceVMX, path.Join("/vmfs/volumes", d.UUID)+"/") {
			source = d
		}
	}
	r.SourceDatastore = source.Name
	if !source.Mounted || !esxi.SupportedVMFS(source.Type) {
		r.check("Source datastore", "BLOCK", "Source must be a mounted VMFS-5 or VMFS-6 datastore")
	}
	if source.UUID == target.UUID {
		r.check("Target datastore", "BLOCK", "Select a different datastore")
	}
	targetMount, e := a.Host.Canonical(ctx, path.Join("/vmfs/volumes", target.UUID))
	if e != nil {
		return r, e
	}
	if targetMount != path.Join("/vmfs/volumes", target.UUID) {
		return r, fmt.Errorf("target datastore UUID path is not canonical")
	}
	// A folder named after the source VM is what an administrator expects to
	// find on the target. Uniqueness comes from refusing to touch an existing
	// directory, not from burying a job ID in its name.
	folder := strings.TrimSpace(req.TargetName)
	if folder == "" {
		folder = path.Base(r.SourceDir)
	}
	if e := esxi.ValidPath(folder); e != nil || strings.Contains(folder, "/") || strings.HasPrefix(folder, ".") {
		return r, fmt.Errorf("target folder name must be a plain directory name")
	}
	r.TargetDir = path.Join(targetMount, folder)
	r.TargetVMX = path.Join(r.TargetDir, path.Base(r.SourceVMX))
	r.TargetFree = target.Free
	if !esxi.TargetPath(r.TargetDir) {
		r.check("Target directory", "BLOCK", "The target folder must be a direct child of the target datastore")
	}
	exists, e := a.Host.Exists(ctx, r.TargetDir)
	if e != nil {
		return r, e
	}
	if exists && !targetCreated {
		detail := folder + " already exists on the target datastore and is never overwritten."
		if free, e := a.freeFolder(ctx, targetMount, folder); e == nil && free != "" {
			detail += " " + free + " is free; enter it as the target folder."
		}
		r.check("Target directory", "BLOCK", detail)
	}
	r.Power, e = a.Host.Power(ctx, req.VMID)
	if e != nil {
		return r, e
	}
	if r.Power == esxi.Suspended {
		r.check("Power state", "BLOCK", "Suspended VM is unsupported")
	}
	raw, e := a.Host.ReadFile(ctx, r.SourceVMX)
	if e != nil {
		return r, e
	}
	r.Config, e = vmx.Parse(raw)
	if e != nil {
		return r, e
	}
	configAnalysis := vmx.Analyze(r.Config)
	for _, b := range configAnalysis.Blocks {
		r.check("VM configuration", "BLOCK", b)
	}
	if a.ApplianceUUID != "" && sameUUID(r.Config["uuid.bios"], a.ApplianceUUID) {
		r.check("Mover appliance", "BLOCK", "Migrating the ESXi Mover appliance itself is unsupported")
	}
	snap, e := a.Host.Snapshot(ctx, req.VMID)
	if e != nil {
		return r, e
	}
	if e = SnapshotManager(snap); e != nil {
		r.check("Snapshot Manager", "BLOCK", e.Error())
	} else {
		r.check("Snapshot Manager", "OK", "No snapshot tree reported")
	}
	files, e := a.Host.List(ctx, r.SourceDir)
	if e != nil {
		return r, e
	}
	if e = SnapshotFiles(files); e != nil {
		r.check("Snapshot artifacts", "BLOCK", e.Error())
	} else {
		r.check("Snapshot artifacts", "OK", "No delta, seSparse, numbered snapshot or suspend files")
	}
	for _, f := range files {
		if strings.EqualFold(path.Ext(f), ".vmsd") {
			s, e := a.Host.ReadFile(ctx, f)
			if e != nil {
				return r, e
			}
			if e = VMSD(s); e != nil {
				r.check("VMSD metadata", "BLOCK", e.Error())
			} else {
				r.check("VMSD metadata", "OK", "Empty snapshot metadata")
			}
		}
	}
	replacements := map[string]string{}
	backings := map[string]bool{}
	destNames := map[string]bool{path.Base(r.SourceVMX): true}
	fingerprints := []string{}
	for _, ref := range configAnalysis.Disks {
		src, e := a.localFile(ctx, ref.File, r.SourceDir, inv.Datastores)
		if e != nil {
			r.check("VMDK location", "BLOCK", e.Error())
			continue
		}
		if backings[src] {
			r.check("Shared VMDK", "BLOCK", "Multiple devices reference the same disk")
		}
		backings[src] = true
		name := path.Base(src)
		extentName := strings.TrimSuffix(name, path.Ext(name)) + "-flat.vmdk"
		if destNames[name] || destNames[extentName] {
			r.check("Target filename", "BLOCK", "Target filename collision")
		}
		destNames[name] = true
		destNames[extentName] = true
		text, e := a.Host.ReadFile(ctx, src)
		if e != nil {
			r.check("VMDK descriptor", "BLOCK", "Unreadable descriptor")
			continue
		}
		desc, e := vmdk.Parse(text)
		if e != nil {
			r.check("VMDK descriptor", "BLOCK", e.Error())
			continue
		}
		if e = DiskSnapshot(src, desc); e != nil {
			r.check("Active snapshot chain", "BLOCK", e.Error())
		}
		if e = desc.Standalone(); e != nil {
			r.check("VMDK format", "BLOCK", e.Error())
			continue
		}
		extent, e := a.localFile(ctx, desc.Extents[0].File, r.SourceDir, inv.Datastores)
		if e != nil {
			r.check("Disk extent", "BLOCK", e.Error())
			continue
		}
		if backings[extent] {
			r.check("Shared extent", "BLOCK", "Multiple disks share the same extent")
		}
		backings[extent] = true
		size, e := a.Host.Size(ctx, extent)
		if e != nil || size != desc.Bytes {
			r.check("Disk extent", "BLOCK", "Extent is missing or its logical capacity differs from the descriptor")
		}
		allocated, ae := a.Host.Allocated(ctx, extent)
		known := ae == nil && allocated <= desc.Bytes && allocated >= 0
		if !known {
			allocated = desc.Bytes
		}
		disk := Disk{ref.Key, src, path.Join(r.TargetDir, name), extent, desc.Bytes, allocated, known, desc.Thin, desc}
		r.Disks = append(r.Disks, disk)
		replacements[ref.Key] = name
		if r.Provisioned > math.MaxInt64-desc.Bytes {
			return r, fmt.Errorf("capacity overflow")
		}
		r.Provisioned += desc.Bytes
		r.Allocated += allocated
		fingerprints = append(fingerprints, src+"\n"+text+"\n"+extent)
		if r.Power == esxi.Off {
			if e = a.Host.VerifyChain(ctx, src); e != nil {
				r.check("Source disk chain", "BLOCK", e.Error())
			}
		}
	}
	if len(r.Disks) == 0 {
		r.check("Disks", "BLOCK", "No safe disks found")
	}
	for _, key := range []string{"nvram", "extendedconfigfile"} {
		if ref := r.Config[key]; ref != "" {
			src, e := a.localFile(ctx, ref, r.SourceDir, inv.Datastores)
			if e != nil {
				r.check("Configuration files", "BLOCK", e.Error())
				continue
			}
			ext := strings.ToLower(path.Ext(src))
			if (key == "nvram" && ext != ".nvram") || (key == "extendedconfigfile" && ext != ".vmxf") {
				r.check("Configuration files", "BLOCK", "Unexpected configuration file extension")
				continue
			}
			if destNames[path.Base(src)] {
				r.check("Configuration files", "BLOCK", "Target configuration name collision")
			}
			destNames[path.Base(src)] = true
			metadata, readErr := a.Host.ReadFile(ctx, src)
			if readErr != nil {
				r.check("Configuration files", "BLOCK", "Configuration file cannot be read")
				continue
			}
			if key == "extendedconfigfile" {
				if err := vmx.ValidateVMXF(metadata, path.Base(r.SourceVMX)); err != nil {
					r.check("VMXF metadata", "BLOCK", err.Error())
					continue
				}
			}
			r.ConfigFiles = append(r.ConfigFiles, ConfigFile{key, src, path.Base(src)})
			replacements[key] = path.Base(src)
		}
	}
	for _, ref := range configAnalysis.References {
		if ref.Kind == "ISO" {
			full, e := esxi.ResolveReference(ref.File, r.SourceDir, inv.Datastores)
			if e != nil {
				r.check("ISO reference", "BLOCK", e.Error())
				continue
			}
			replacements[ref.Key] = full
			r.check("External ISO", "WARNING", full+" remains on its existing datastore")
		}
	}
	// Unknown path-bearing keys could make the target write into the source.
	for k, v := range r.Config {
		if _, ok := replacements[k]; ok {
			continue
		}
		if strings.Contains(v, "/vmfs/") || strings.HasPrefix(v, "[") {
			switch k {
			case "sched.swap.derivedname", "workingdir", "snapshot.directory", "checkpoint.vmstate", "migrate.hostlog", "log.filename":
			default:
				r.check("External configuration reference", "BLOCK", "Unclassified datastore path in "+k)
			}
		}
	}
	if e = a.sharedCheck(ctx, inv, r.VM.ID, backings); e != nil {
		r.check("Shared disk inventory", "BLOCK", e.Error())
	} else {
		r.check("Shared disk inventory", "OK", "No other registered VM references the disk or extent")
	}
	r.TargetConfig, e = vmx.Rewrite(r.Config, replacements)
	if e != nil {
		return r, e
	}
	// uuid.action = keep is VMware's own answer "I moved it": the host keeps
	// the identity and never stops the power-on to ask. A COPY is left alone,
	// because a copy run beside its original needs "I copied it".
	if req.Mode == "MOVE" {
		r.TargetConfig["uuid.action"] = "keep"
	}
	stable := stableConfig(r.Config)
	sort.Strings(fingerprints)
	sum := sha256.Sum256([]byte(r.SourceVMX + "\n" + stable.String() + "\n" + strings.Join(fingerprints, "\n")))
	r.Fingerprint = hex.EncodeToString(sum[:])
	// A thin disk is cloned thin, so the clone writes roughly what is allocated
	// rather than what is provisioned. r.Allocated already falls back to the
	// provisioned size for any disk whose allocation could not be read, so it
	// is the honest requirement. Holding the full provisioned size is a
	// separate concern: the disks can still grow after the move, so say so
	// instead of refusing the migration outright.
	remaining := r.Allocated - consumed
	if remaining < 0 {
		remaining = 0
	}
	r.Required, e = reserve(remaining)
	if e != nil {
		return r, e
	}
	grown := r.Provisioned - consumed
	if grown < 0 {
		grown = 0
	}
	full, e := reserve(grown)
	if e != nil {
		return r, e
	}
	switch {
	case r.TargetFree < r.Required:
		r.check("Free space", "BLOCK", "Insufficient free space for allocated capacity + 15% + 1 GiB")
	case r.TargetFree < full:
		r.check("Free space", "WARNING", "Enough for the thin clone, but the target cannot hold these disks once they grow to their full provisioned size")
	default:
		r.check("Free space", "OK", "Reserved estimate uses allocated capacity + 15% + 1 GiB")
	}
	if r.Ready {
		r.check("Migration", "OK", "Ready for a fresh preflight and cold migration")
	}
	return r, nil
}
func normalizeUUID(s string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "-", "").Replace(s))
}
func sameUUID(bios, appliance string) bool {
	a, b := normalizeUUID(bios), normalizeUUID(appliance)
	if len(a) != 32 || len(b) != 32 {
		return false
	}
	if a == b {
		return true
	}
	// SMBIOS 2.6 product UUID uses little endian for the first three fields.
	flipped := b[6:8] + b[4:6] + b[2:4] + b[0:2] + b[10:12] + b[8:10] + b[14:16] + b[12:14] + b[16:]
	return a == flipped
}
func stableConfig(c vmx.Config) vmx.Config {
	out, _ := vmx.Rewrite(c, map[string]string{})
	for _, k := range []string{"cleanshutdown", "softpoweroff", "tools.lastinstallstatus", "tools.remindinstall"} {
		delete(out, k)
	}
	return out
}
func (a Analyzer) localFile(ctx context.Context, ref, dir string, ds []esxi.Datastore) (string, error) {
	p, e := esxi.ResolveReference(ref, dir, ds)
	if e != nil {
		return "", e
	}
	canonical, e := a.Host.Canonical(ctx, p)
	if e != nil {
		return "", e
	}
	if path.Dir(canonical) != dir {
		return "", fmt.Errorf("files outside the VM directory (including other datastores and nested directories) are unsupported")
	}
	exists, e := a.Host.Exists(ctx, canonical)
	if e != nil || !exists {
		return "", fmt.Errorf("required file is missing or inaccessible")
	}
	return canonical, nil
}
// freeFolder offers the first unused variant of a name so a collision leaves
// the operator with an answer rather than a puzzle.
func (a Analyzer) freeFolder(ctx context.Context, mount, folder string) (string, error) {
	for i := 2; i <= 9; i++ {
		candidate := fmt.Sprintf("%s-%d", folder, i)
		used, e := a.Host.Exists(ctx, path.Join(mount, candidate))
		if e != nil {
			return "", e
		}
		if !used {
			return candidate, nil
		}
	}
	return "", nil
}

func (a Analyzer) sharedCheck(ctx context.Context, inv esxi.Inventory, id int, backings map[string]bool) error {
	for _, other := range inv.VMs {
		if other.ID == id {
			continue
		}
		p, e := esxi.ResolveReference(other.VMXPath, "", inv.Datastores)
		if e != nil {
			return e
		}
		p, e = a.Host.Canonical(ctx, p)
		if e != nil {
			return fmt.Errorf("cannot inspect another registered VM")
		}
		raw, e := a.Host.ReadFile(ctx, p)
		if e != nil {
			return fmt.Errorf("cannot inspect another registered VM")
		}
		cfg, e := vmx.Parse(raw)
		if e != nil {
			return fmt.Errorf("another VM has unrecognized configuration; shared disks cannot be excluded")
		}
		for k, v := range cfg {
			if !strings.HasSuffix(strings.ToLower(v), ".vmdk") {
				continue
			}
			if !strings.HasSuffix(k, ".filename") {
				return fmt.Errorf("unclassified disk dependency in another VM")
			}
			ref, e := esxi.ResolveReference(v, path.Dir(p), inv.Datastores)
			if e != nil {
				return e
			}
			ref, e = a.Host.Canonical(ctx, ref)
			if e != nil {
				return e
			}
			if backings[ref] {
				return fmt.Errorf("another registered VM references a source disk")
			}
			text, e := a.Host.ReadFile(ctx, ref)
			if e != nil {
				return fmt.Errorf("cannot exclude shared disk backing")
			}
			d, e := vmdk.Parse(text)
			if e != nil {
				return fmt.Errorf("unreadable other-VM descriptor; cannot exclude shared backing")
			}
			if e := a.otherChain(ctx, inv, ref, d, backings); e != nil {
				return e
			}
		}
	}
	return nil
}

// otherChain walks one other VM's disk chain. A snapshot in another VM is only
// a dependency risk when the chain can reach the source VM's files, so follow
// it instead of refusing outright: block on any link that touches a source
// backing, and block when a link leaves the other VM's own directory, where
// isolation can no longer be proven.
func (a Analyzer) otherChain(ctx context.Context, inv esxi.Inventory, ref string, d vmdk.Descriptor, backings map[string]bool) error {
	dir := path.Dir(ref)
	for depth := 0; depth <= 32; depth++ {
		for _, ex := range d.Extents {
			ep, e := esxi.ResolveReference(ex.File, dir, inv.Datastores)
			if e != nil {
				return e
			}
			ep, e = a.Host.Canonical(ctx, ep)
			if e != nil {
				return e
			}
			if backings[ep] {
				return fmt.Errorf("another registered VM shares a source extent")
			}
			if path.Dir(ep) != dir {
				return fmt.Errorf("another VM's extent leaves its own directory; isolation cannot be proven")
			}
		}
		if strings.EqualFold(d.ParentCID, "ffffffff") && d.ParentHint == "" {
			return nil
		}
		if d.ParentHint == "" {
			return fmt.Errorf("another VM has a parent chain with no resolvable parent; isolation cannot be proven")
		}
		parent, e := esxi.ResolveReference(d.ParentHint, dir, inv.Datastores)
		if e != nil {
			return e
		}
		parent, e = a.Host.Canonical(ctx, parent)
		if e != nil {
			return e
		}
		if backings[parent] {
			return fmt.Errorf("another VM's snapshot chain references a source disk")
		}
		if path.Dir(parent) != dir {
			return fmt.Errorf("another VM's chain leaves its own directory; isolation cannot be proven")
		}
		text, e := a.Host.ReadFile(ctx, parent)
		if e != nil {
			return fmt.Errorf("cannot read a parent in another VM's chain")
		}
		d, e = vmdk.Parse(text)
		if e != nil {
			return fmt.Errorf("unreadable parent descriptor in another VM's chain")
		}
	}
	return fmt.Errorf("another VM's disk chain is too deep to verify")
}
