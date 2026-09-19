package vmx

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type DiskRef struct {
	Key        string
	File       string
	Controller string
}
type Reference struct {
	Key  string
	File string
	Kind string
}
type Analysis struct {
	Disks       []DiskRef
	References  []Reference
	Blocks      []string
	Controllers []string
}

var diskKey = regexp.MustCompile(`^(scsi|sata|ide|nvme)[0-9]+:[0-9]+\.filename$`)

func Analyze(c Config) Analysis {
	a := Analysis{}
	for k, v := range c {
		low := strings.ToLower(v)
		if k == "uuid.action" && low != "" && low != "keep" {
			a.Blocks = append(a.Blocks, "VMX forces an identity change; uuid.action requires manual review")
		}
		if strings.Contains(k, "encrypt") || strings.Contains(k, "vtpm") || strings.HasPrefix(k, "tpm.") || strings.Contains(k, "keyid") {
			a.Blocks = append(a.Blocks, "Encryption or vTPM configuration: "+k)
		}
		if (strings.Contains(k, "sharing") && low != "none" && low != "") || strings.Contains(low, "multi-writer") || (strings.HasSuffix(k, "sharedbus") && low != "none") {
			a.Blocks = append(a.Blocks, "Shared or multi-writer disk configuration: "+k)
		}
		if strings.HasSuffix(k, ".mode") && (strings.HasPrefix(k, "scsi") || strings.HasPrefix(k, "sata") || strings.HasPrefix(k, "ide") || strings.HasPrefix(k, "nvme")) && low != "persistent" && low != "" {
			a.Blocks = append(a.Blocks, "Only ordinary persistent disks are supported: "+k)
		}
		if strings.HasSuffix(k, ".redo") && v != "" {
			a.Blocks = append(a.Blocks, "Redo disk reference: "+k)
		}
		if (strings.Contains(k, "ctk") && low == "true") || (k == "checkpoint.vmstate" && v != "") {
			a.Blocks = append(a.Blocks, "CBT or suspended state is unsupported: "+k)
		}
		if strings.HasSuffix(k, ".virtualdev") {
			a.Controllers = append(a.Controllers, k+"="+v)
		}
		if strings.HasSuffix(k, ".devicetype") && (strings.Contains(low, "raw") || strings.Contains(low, "passthru")) {
			a.Blocks = append(a.Blocks, "Raw or passthrough device: "+k)
		}
		if strings.HasPrefix(k, "pcipassthru") && strings.HasSuffix(k, ".present") && low == "true" {
			a.Blocks = append(a.Blocks, "PCI passthrough is unsupported")
		}
		if strings.HasSuffix(k, ".filename") {
			base := strings.TrimSuffix(k, ".filename")
			if strings.HasSuffix(low, ".vmdk") {
				if !diskKey.MatchString(k) || !c.True(base+".present") {
					a.Blocks = append(a.Blocks, "Unrecognized or inactive VMDK reference: "+k)
					continue
				}
				a.Disks = append(a.Disks, DiskRef{k, v, strings.Split(base, ":")[0]})
			} else {
				kind := "external"
				switch {
				case strings.HasSuffix(low, ".iso"):
					kind = "ISO"
				case strings.HasPrefix(k, "floppy"):
					kind = "floppy"
				case strings.HasPrefix(k, "serial") || strings.HasPrefix(k, "parallel"):
					kind = "serial/parallel"
				}
				if kind == "serial/parallel" && strings.EqualFold(c[base+".filetype"], "file") {
					a.Blocks = append(a.Blocks, "Writable serial/parallel file is unsupported: "+k)
				}
				if kind == "floppy" && c.True(base+".present") {
					a.Blocks = append(a.Blocks, "Connected floppy devices are unsupported")
				}
				if c.True(base+".present") && kind == "external" {
					a.Blocks = append(a.Blocks, "Unrecognized active device backing: "+k)
				}
				a.References = append(a.References, Reference{k, v, kind})
			}
		}
	}
	if len(a.Disks) == 0 {
		a.Blocks = append(a.Blocks, "No supported active VMDK disks found")
	}
	sort.Slice(a.Disks, func(i, j int) bool { return a.Disks[i].Key < a.Disks[j].Key })
	sort.Slice(a.References, func(i, j int) bool { return a.References[i].Key < a.References[j].Key })
	sort.Strings(a.Blocks)
	sort.Strings(a.Controllers)
	return a
}

// Rewrite only explicit disk/config backings and known generated runtime paths.
// External media remains on the source datastore; callers resolve relative ISO paths.
func Rewrite(c Config, replacements map[string]string) (Config, error) {
	out := c.Clone()
	for k, v := range replacements {
		if _, ok := out[k]; !ok {
			return nil, fmt.Errorf("missing rewrite key %s", k)
		}
		out[k] = v
	}
	for k := range out {
		if k == "sched.swap.derivedname" || k == "workingdir" || k == "snapshot.directory" || k == "checkpoint.vmstate" || k == "migrate.hostlog" || k == "log.filename" {
			delete(out, k)
		}
	}
	return out, nil
}
