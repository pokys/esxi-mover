package migration

import (
	"context"
	"fmt"
	"path"
	"strings"

	"esxi-mover/internal/vmdk"
	"esxi-mover/internal/vmx"
)

func verifyDisk(ctx context.Context, h Host, d Disk) error {
	if e := h.VerifyChain(ctx, d.Target); e != nil {
		return e
	}
	raw, e := h.ReadFile(ctx, d.Target)
	if e != nil {
		return e
	}
	desc, e := vmdk.Parse(raw)
	if e != nil {
		return e
	}
	if e = DiskSnapshot(d.Target, desc); e != nil {
		return e
	}
	if e = desc.Standalone(); e != nil {
		return e
	}
	if !desc.Thin || desc.Bytes != d.Provisioned {
		return fmt.Errorf("target disk is not thin or its virtual capacity changed")
	}
	ex := desc.Extents[0].File
	if path.Base(ex) != ex || strings.ContainsAny(ex, "\r\n\x00") {
		return fmt.Errorf("target extent is not local to its descriptor")
	}
	expected := path.Join(path.Dir(d.Target), ex)
	canonical, e := h.Canonical(ctx, expected)
	if e != nil || canonical != expected {
		return fmt.Errorf("target extent escaped target directory")
	}
	n, e := h.Size(ctx, expected)
	if e != nil || n != d.Provisioned {
		return fmt.Errorf("target extent size mismatch")
	}
	return nil
}

// diskNames maps each disk's VMX key to the file the target VMX must name:
// the cloned base disk, or for a live cutover the copied snapshot delta.
func diskNames(disks []Disk, deltas map[string]string) map[string]string {
	names := map[string]string{}
	for _, d := range disks {
		names[d.Key] = path.Base(d.Target)
		if n, ok := deltas[d.Key]; ok {
			names[d.Key] = n
		}
	}
	return names
}
func copyConfig(ctx context.Context, h Host, r Report, config vmx.Config, names map[string]string) error {
	for _, f := range r.ConfigFiles {
		data, e := h.ReadFile(ctx, f.Source)
		if e != nil {
			return e
		}
		if e = h.WriteTarget(ctx, r.TargetDir, f.Name, []byte(data)); e != nil {
			return e
		}
		target, e := h.ReadFile(ctx, path.Join(r.TargetDir, f.Name))
		if e != nil || target != data {
			return fmt.Errorf("target configuration file verification failed")
		}
	}
	serialized := config.String()
	if e := h.WriteTarget(ctx, r.TargetDir, path.Base(r.TargetVMX), []byte(serialized)); e != nil {
		return e
	}
	raw, e := h.ReadFile(ctx, r.TargetVMX)
	if e != nil {
		return e
	}
	parsed, e := vmx.Parse(raw)
	if e != nil {
		return e
	}
	if parsed.String() != serialized {
		return fmt.Errorf("target VMX does not match the reviewed configuration")
	}
	a := vmx.Analyze(parsed)
	if len(a.Blocks) > 0 || len(a.Disks) != len(r.Disks) {
		return fmt.Errorf("rewritten VMX has unsupported devices or missing disks")
	}
	for _, d := range r.Disks {
		if parsed[d.Key] != names[d.Key] {
			return fmt.Errorf("target VMX points to the wrong disk")
		}
	}
	return nil
}
