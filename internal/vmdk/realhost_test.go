package vmdk

import "testing"

// A thin disk descriptor as ESXi 8.0 Update 1 writes it, with its identifiers
// replaced: it adds ddb.deletable, and "# The Disk Data Base " ends in a blank.
const descriptor80 = `# Disk DescriptorFile
version=1
encoding="UTF-8"
CID=00000001
parentCID=ffffffff
createType="vmfs"

# Extent description
RW 167772160 VMFS "vm1-flat.vmdk"

# The Disk Data Base
#DDB

ddb.adapterType = "lsilogic"
ddb.deletable = "true"
ddb.geometry.cylinders = "10443"
ddb.geometry.heads = "255"
ddb.geometry.sectors = "63"
ddb.longContentID = "00000000000000000000000000000000"
ddb.thinProvisioned = "1"
ddb.toolsInstallType = "1"
ddb.toolsVersion = "13349"
ddb.uuid = "56 4d 00 00 00 00 00 00-00 00 00 00 00 00 00 00"
ddb.virtualHWVersion = "14"
`

func TestRealESXi80Descriptor(t *testing.T) {
	d, e := Parse(descriptor80)
	if e != nil {
		t.Fatal(e)
	}
	if e = d.Standalone(); e != nil {
		t.Fatal(e)
	}
	if !d.Thin || d.Bytes != 167772160*512 || d.Extents[0].File != "vm1-flat.vmdk" {
		t.Fatalf("descriptor: %+v", d)
	}
}
