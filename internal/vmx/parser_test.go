package vmx

import (
	"os"
	"strings"
	"testing"
)

func TestParseRoundTrip(t *testing.T) {
	raw, e := os.ReadFile("../../fixtures/standard.vmx")
	if e != nil {
		t.Fatal(e)
	}
	c, e := Parse(string(raw))
	if e != nil {
		t.Fatal(e)
	}
	a := Analyze(c)
	if len(a.Disks) != 2 || len(a.Blocks) != 0 || len(a.References) != 1 {
		t.Fatalf("unexpected analysis: %+v", a)
	}
	for _, value := range []string{"normal", "VM Name", "VM's Server", "$(command)", "; rm ignored", "&", "\"", "'", "`", "line\nnext", "pipe|value"} {
		c["displayname"] = value
		again, e := Parse(c.String())
		if e != nil || again["displayname"] != value {
			t.Fatalf("roundtrip %q: %v", value, e)
		}
	}
}
func TestRejectAmbiguousVMX(t *testing.T) {
	for _, raw := range []string{"", "broken", `a="1"` + "\n" + `A="2"`, `a="unterminated`, `a="one" garbage`, `a="|xx"`, `.encoding="windows-1252"`} {
		if _, e := Parse(raw); e == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
func TestUnsupportedFeatures(t *testing.T) {
	for name, extra := range map[string]string{"multiwriter": `scsi0:0.sharing = "multi-writer"`, "sharedbus": `scsi0.sharedBus = "virtual"`, "vtpm": `vtpm.present = "TRUE"`, "encrypted": `encryption.keySafe = "fixture"`, "cbt": `ctkEnabled = "TRUE"`, "rdm": `scsi0:0.deviceType = "scsi-passthru"`, "independent": `scsi0:0.mode = "independent-persistent"`} {
		t.Run(name, func(t *testing.T) {
			c, e := Parse("scsi0:0.present = \"TRUE\"\nscsi0:0.fileName = \"disk.vmdk\"\n" + extra)
			if e != nil {
				t.Fatal(e)
			}
			if len(Analyze(c).Blocks) == 0 {
				t.Fatal("unsupported feature accepted")
			}
		})
	}
}
func TestRewritePreservesControllerAndIdentity(t *testing.T) {
	c, _ := Parse("scsi0.virtualDev = \"pvscsi\"\nscsi0:0.fileName = \"/vmfs/volumes/source/vm/disk.vmdk\"\nuuid.bios = \"fixture-uuid\"\nsched.swap.derivedName = \"old.vswp\"")
	out, e := Rewrite(c, map[string]string{"scsi0:0.filename": "disk.vmdk"})
	if e != nil {
		t.Fatal(e)
	}
	if out["scsi0.virtualdev"] != "pvscsi" || out["uuid.bios"] != "fixture-uuid" || out["sched.swap.derivedname"] != "" || !strings.Contains(c["scsi0:0.filename"], "source") {
		t.Fatal("rewrite damaged identity or source config")
	}
}
func TestVMXF(t *testing.T) {
	if e := ValidateVMXF(`<Foundry><VM><vmxPathName type="string">vm.vmx</vmxPathName></VM></Foundry>`, "vm.vmx"); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{`<Foundry><vmxPathName>/vmfs/volumes/old/vm.vmx</vmxPathName></Foundry>`, `<!DOCTYPE x><Foundry/>`, `<Foundry><vmxPathName>other.vmx</vmxPathName></Foundry>`, `<bad/>`, `<Foundry><VM><vmxPathName>vm.vmx</vmxPathName><Team>other.vmx</Team></VM></Foundry>`, `<Foundry><VM><vmxPathName/></VM></Foundry>`, `<?xml-stylesheet href="external"?><Foundry><VM><vmxPathName>vm.vmx</vmxPathName></VM></Foundry>`} {
		if ValidateVMXF(s, "vm.vmx") == nil {
			t.Fatal("unsafe VMXF accepted")
		}
	}
}
func FuzzVMXRoundTrip(f *testing.F) {
	f.Add("vm's $disk |22")
	f.Fuzz(func(t *testing.T, value string) {
		if strings.ContainsRune(value, 0) {
			return
		}
		c := Config{"test": value}
		p, e := Parse(c.String())
		if e == nil && p["test"] != value {
			t.Fatal("non-exact roundtrip")
		}
	})
}
