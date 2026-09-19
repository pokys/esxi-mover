package esxi

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

type VM struct {
	ID                       int
	Name, Datastore, VMXPath string
}
type Datastore struct {
	Name, UUID, Mount, Type string
	Mounted                 bool
	Size, Free              int64
}

// VMFS-L is ESXi's system filesystem, not a supported VM datastore.
// Unknown future VMFS versions require an explicit capability review.
func SupportedVMFS(kind string) bool { return kind == "VMFS-5" || kind == "VMFS-6" }

type Capabilities struct {
	Version   string
	Supported bool
	Commands  []string
}
type Inventory struct {
	Capabilities      Capabilities
	VMs               []VM
	Datastores        []Datastore
	ExistingOperation string
}

var vmRow = regexp.MustCompile(`^\s*(\d+)\s+(.+?)\s+\[([^\]\r\n]+)\]\s+(.+?\.vmx)\s+\S+\s+vmx-\d+(?:\s+.*)?$`)
var spaces = regexp.MustCompile(`\s{2,}`)
var versionPattern = regexp.MustCompile(`VMware ESXi (6\.[57]|7\.\d|8\.\d)\.`)

func ParseVMs(s string) ([]VM, error) {
	out := []VM{}
	header := false
	ids := map[int]bool{}
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "Vmid") && strings.Contains(line, "File") {
			if header {
				return nil, fmt.Errorf("duplicate inventory header")
			}
			header = true
			continue
		}
		m := vmRow.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("unrecognized VM inventory row; inventory may include invalid VMs")
		}
		id, e := strconv.Atoi(m[1])
		if e != nil || id <= 0 || ids[id] {
			return nil, fmt.Errorf("invalid VM ID")
		}
		ids[id] = true
		if e := ValidPath(m[4]); e != nil {
			return nil, e
		}
		if path.IsAbs(m[4]) {
			return nil, fmt.Errorf("unexpected absolute inventory path")
		}
		out = append(out, VM{id, strings.TrimSpace(m[2]), m[3], "[" + m[3] + "] " + m[4]})
	}
	if !header {
		return nil, fmt.Errorf("missing VM inventory header")
	}
	return out, nil
}
func positiveNumber(s string) (int64, error) {
	n, e := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if e != nil || n < 0 {
		return 0, fmt.Errorf("unknown numeric output")
	}
	return n, nil
}
func ParseDatastores(s string) ([]Datastore, error) {
	out := []Datastore{}
	header := false
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "Mount Point") && strings.Contains(t, "Free") {
			header = true
			continue
		}
		if strings.Trim(t, "- ") == "" {
			continue
		}
		f := spaces.Split(t, -1)
		// Bootbank/VFAT rows often have an empty volume name; an unmounted
		// filesystem may instead have an empty mount point.
		if len(f) == 6 && (f[2] == "true" || f[2] == "false") {
			if strings.HasPrefix(f[0], "/vmfs/volumes/") {
				f = append([]string{f[0], ""}, f[1:]...)
			} else if f[2] == "false" {
				f = append([]string{""}, f...)
			}
		}
		if len(f) != 7 {
			return nil, fmt.Errorf("unknown datastore table format")
		}
		size, e := positiveNumber(f[5])
		if e != nil {
			return nil, e
		}
		free, e := positiveNumber(f[6])
		if e != nil || free > size {
			return nil, fmt.Errorf("invalid datastore capacity")
		}
		if f[3] != "true" && f[3] != "false" {
			return nil, fmt.Errorf("unknown datastore mounted state")
		}
		out = append(out, Datastore{f[1], f[2], f[0], f[4], f[3] == "true", size, free})
	}
	if !header || len(out) == 0 {
		return nil, fmt.Errorf("no recognized datastore inventory")
	}
	return out, nil
}
func (c *Client) VMs(ctx context.Context) ([]VM, error) {
	s, e := c.command(ctx, "inventory", "vim-cmd", "vmsvc/getallvms")
	if e != nil {
		return nil, e
	}
	vms, err := ParseVMs(s)
	if err != nil {
		return nil, err
	}
	for _, vm := range vms {
		actual, err := c.VMPath(ctx, vm.ID)
		if err != nil {
			return nil, err
		}
		if actual != vm.VMXPath {
			return nil, fmt.Errorf("VM inventory and authoritative configuration path disagree")
		}
	}
	return vms, nil
}
func (c *Client) Datastores(ctx context.Context) ([]Datastore, error) {
	s, e := c.command(ctx, "datastores", "esxcli", "storage", "filesystem", "list")
	if e != nil {
		return nil, e
	}
	return ParseDatastores(s)
}
func (c *Client) Inventory(ctx context.Context) (Inventory, error) {
	var i Inventory
	v, e := c.command(ctx, "version", "vmware", "-vl")
	if e != nil {
		return i, e
	}
	i.Capabilities.Version = strings.TrimSpace(v)
	i.Capabilities.Supported = versionPattern.MatchString(v)
	for _, name := range []string{"vim-cmd", "vmkfstools", "esxcli", "readlink", "find", "stat", "du", "nohup", "sh", "head", "tail", "mkdir", "mv", "cat", "test", "kill"} {
		if _, e := c.run(ctx, "capability", "command -v "+Quote(name), nil); e != nil {
			return i, fmt.Errorf("required ESXi command unavailable: %s", name)
		}
		i.Capabilities.Commands = append(i.Capabilities.Commands, name)
	}
	i.VMs, e = c.VMs(ctx)
	if e != nil {
		return i, e
	}
	i.Datastores, e = c.Datastores(ctx)
	if e != nil {
		return i, e
	}
	i.ExistingOperation, e = c.Existing(ctx)
	return i, e
}
func ValidPath(p string) error {
	if p == "" || strings.ContainsAny(p, "\x00\r\n\\") {
		return fmt.Errorf("empty, control-character or backslash path is unsupported")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." {
			return fmt.Errorf("path traversal is unsupported")
		}
	}
	return nil
}
func ResolveReference(ref, dir string, ds []Datastore) (string, error) {
	if e := ValidPath(ref); e != nil {
		return "", e
	}
	if strings.HasPrefix(ref, "[") {
		end := strings.Index(ref, "] ")
		if end < 2 {
			return "", fmt.Errorf("invalid datastore reference")
		}
		name := ref[1:end]
		rest := ref[end+2:]
		if path.IsAbs(rest) || rest == "" {
			return "", fmt.Errorf("invalid relative datastore reference")
		}
		found := ""
		for _, d := range ds {
			if d.Name == name || d.UUID == name {
				if found != "" {
					return "", fmt.Errorf("ambiguous datastore name")
				}
				found = path.Join("/vmfs/volumes", d.UUID, rest)
			}
		}
		if found == "" {
			return "", fmt.Errorf("referenced datastore not found")
		}
		return found, nil
	}
	if path.IsAbs(ref) {
		if !strings.HasPrefix(ref, "/vmfs/volumes/") {
			return "", fmt.Errorf("reference outside datastores")
		}
		return ref, nil
	}
	if dir == "" {
		return "", fmt.Errorf("relative path has no base directory")
	}
	return path.Join(dir, ref), nil
}
