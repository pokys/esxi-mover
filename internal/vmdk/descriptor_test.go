package vmdk

import (
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	b, e := os.ReadFile("../../fixtures/thin.vmdk")
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func TestThinThickAndCapacity(t *testing.T) {
	raw := fixture(t)
	d, e := Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	if !d.Thin || d.Bytes != 1<<30 || d.Standalone() != nil {
		t.Fatalf("bad thin descriptor: %+v", d)
	}
	d, e = Parse(strings.ReplaceAll(raw, `ddb.thinProvisioned = "1"`, `ddb.thinProvisioned = "0"`))
	if e != nil || d.Thin || d.Standalone() != nil {
		t.Fatal("thick descriptor rejected")
	}
}
func TestUnsafeDescriptors(t *testing.T) {
	raw := fixture(t)
	for name, data := range map[string]string{"parent": strings.Replace(raw, "parentCID=ffffffff", "parentCID=12345678", 1), "hint": raw + "\nparentFileNameHint=\"parent.vmdk\"", "sesparse": strings.Replace(raw, `createType="vmfs"`, `createType="seSparse"`, 1), "vmfsSparse": strings.Replace(raw, `createType="vmfs"`, `createType="vmfsSparse"`, 1), "rdm": strings.Replace(raw, `createType="vmfs"`, `createType="vmfsRawDeviceMap"`, 1), "physical-rdm": strings.Replace(raw, " VMFS ", " VMFSRDM ", 1), "broken": "bad descriptor", "binary": "\x00\x01", "duplicate": raw + "\nCID=abcdef12", "overflow": strings.Replace(raw, "2097152", "9223372036854775807", 1)} {
		t.Run(name, func(t *testing.T) {
			d, e := Parse(data)
			if e == nil && d.Standalone() == nil {
				t.Fatal("unsafe descriptor accepted")
			}
		})
	}
}
func FuzzDescriptor(f *testing.F) {
	f.Add("version=1\n")
	f.Fuzz(func(t *testing.T, s string) {
		d, e := Parse(s)
		if e == nil && d.Bytes <= 0 {
			t.Fatal("invalid capacity")
		}
	})
}

// ESXi writes a version 3 descriptor for a disk with change block tracking
// enabled. Refusing it made one such VM unreadable, and because the shared
// disk scan reads every other VM's descriptor, that one disk blocked the
// analysis of every other VM on the host.
func TestParseAcceptsAChangeTrackedDescriptor(t *testing.T) {
	text := "# Disk DescriptorFile\n" +
		"version=3\n" +
		"encoding=\"UTF-8\"\n" +
		"CID=21c6c69b\n" +
		"parentCID=ffffffff\n" +
		"isNativeSnapshot=\"no\"\n" +
		"createType=\"vmfs\"\n" +
		"\n" +
		"# Extent description\n" +
		"RW 41963520 VMFS \"vm-flat.vmdk\"\n" +
		"\n" +
		"# Change Tracking File\n" +
		"changeTrackPath=\"vm-ctk.vmdk\"\n" +
		"ddb.thinProvisioned = \"1\"\n"
	d, e := Parse(text)
	if e != nil {
		t.Fatal("a change-tracked descriptor was rejected:", e)
	}
	if !d.Thin || d.Bytes != 41963520*512 || len(d.Extents) != 1 {
		t.Fatalf("descriptor parsed but fields are wrong: %+v", d)
	}
	if e := d.Standalone(); e != nil {
		t.Fatal("a change-tracked standalone disk was not accepted:", e)
	}
	if _, e := Parse("version=9\nCID=21c6c69b\nparentCID=ffffffff\ncreateType=\"vmfs\"\nRW 8 VMFS \"x-flat.vmdk\"\n"); e == nil {
		t.Fatal("an unknown descriptor version was accepted")
	}
}
