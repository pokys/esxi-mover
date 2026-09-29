package esxi

import (
	"context"
	"path"
	"strings"
)

// Which VMs run is shown next to each VM when choosing one. It is read once,
// with a single command, and only for that list: the safety checks read each
// VM's power state themselves and never rely on it.

// ParseRunningVMX reads the configuration file of every running VM from
// esxcli vm process list.
func ParseRunningVMX(s string) []string {
	out := []string{}
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Config File:"); ok {
			if p := strings.TrimSpace(v); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// canonicalVMX writes a VMX path as /vmfs/volumes/<uuid>/..., whichever form
// ESXi used: a datastore reference, a UUID path or a datastore name path.
func canonicalVMX(p string, ds []Datastore) string {
	if strings.HasPrefix(p, "[") {
		if full, e := ResolveReference(p, "", ds); e == nil {
			return full
		}
		return p
	}
	rest, ok := strings.CutPrefix(p, "/vmfs/volumes/")
	if !ok {
		return p
	}
	volume, file, _ := strings.Cut(rest, "/")
	for _, d := range ds {
		if d.Name == volume {
			return path.Join("/vmfs/volumes", d.UUID, file)
		}
	}
	return p
}

// MarkRunning sets Power on every VM. When the running list cannot be read,
// Power stays empty rather than claiming a VM is off.
func (c *Client) MarkRunning(ctx context.Context, vms []VM, ds []Datastore) {
	s, e := c.command(ctx, "running-vms", "esxcli", "vm", "process", "list")
	if e != nil {
		return
	}
	running := map[string]bool{}
	for _, p := range ParseRunningVMX(s) {
		running[canonicalVMX(p, ds)] = true
	}
	for i := range vms {
		vms[i].Power = "off"
		if running[canonicalVMX(vms[i].VMXPath, ds)] {
			vms[i].Power = "on"
		}
	}
}
