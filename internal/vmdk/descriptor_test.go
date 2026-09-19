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
