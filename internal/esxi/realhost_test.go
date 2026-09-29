package esxi

import "testing"

// Output shapes captured from a real ESXi 7.0 Update 3 host with
// scripts/capture-host.sh. Names, UUIDs, the build and the annotations are
// replaced; spacing, trailing blanks and line structure are kept as printed.

const getallvms70 = `Vmid           Name                                      File                                    Guest OS          Version                                                                                                          Annotation
10     vm1       [ds2] vm1_1/vm1_1.vmx       windows8Server64Guest    vmx-17
12     vm2           [ds1] vm2/vm2.vmx               vmwarePhoton64Guest      vmx-13    First line of a note that an appliance image ships with.

   A second paragraph of that note, indented.

another line
13     vm3            [ds1] vm3/vm3.vmx                 centos64Guest            vmx-08
2      vm4             [ds2] vm4/vm4.vmx                       windows2019srv_64Guest   vmx-17
6      vm7   [ds2] vm7/vm7.vmx   other3xLinux64Guest      vmx-10    vm7 Appliance
`

const filesystems70 = `Mount Point                                        Volume Name                                 UUID                                 Mounted  Type             Size           Free
-------------------------------------------------  ------------------------------------------  -----------------------------------  -------  ------  -------------  -------------
/vmfs/volumes/00000000-00000000-0000-000000000001  ds1                                  00000000-00000000-0000-000000000001     true  VMFS-6   118380036096    74551656448
/vmfs/volumes/00000000-00000000-0000-000000000002  ds2                                      00000000-00000000-0000-000000000002     true  VMFS-6  1919045074944  1000291172352
/vmfs/volumes/00000000-00000000-0000-000000000003  OSDATA-00000000-00000000-0000-000000000003  00000000-00000000-0000-000000000003     true  VFFS     128580583424   124806758400
/vmfs/volumes/00000000-00000000-0000-000000000004  BOOTBANK1                                   00000000-00000000-0000-000000000004     true  vfat       4293591040     4122804224
`

// Every field line ends in a comma and a blank on 7.0.
const autostart70 = "(vim.host.AutoStartManager.AutoPowerInfo) [\n" +
	"   (vim.host.AutoStartManager.AutoPowerInfo) {\n" +
	"      key = 'vim.VirtualMachine:3', \n" +
	"      startOrder = 1, \n" +
	"      startDelay = -1, \n" +
	"      waitForHeartbeat = \"systemDefault\", \n" +
	"      startAction = \"powerOn\", \n" +
	"      stopDelay = -1, \n" +
	"      stopAction = \"systemDefault\"\n" +
	"   }, \n" +
	"   (vim.host.AutoStartManager.AutoPowerInfo) {\n" +
	"      key = 'vim.VirtualMachine:13', \n" +
	"      startOrder = 2, \n" +
	"      startAction = \"powerOn\", \n" +
	"      stopAction = \"systemDefault\"\n" +
	"   }\n" +
	"]\n"

const guest70 = "   toolsStatus = \"toolsOld\", \n" +
	"   toolsVersionStatus = \"guestToolsNeedUpgrade\", \n" +
	"   toolsRunningStatus = \"guestToolsRunning\", \n" +
	"   toolsUpdateStatus = (vim.vm.GuestInfo.ToolsUpdateStatus) null, \n" +
	"   guestState = \"running\", \n" +
	"   toolsHealthEvents = <unset>, \n" +
	"      customizationStatus = \"TOOLSDEPLOYPKG_IDLE\", \n"

func TestRealESXi70Shapes(t *testing.T) {
	vms, e := ParseVMs(getallvms70)
	if e != nil {
		t.Fatal(e)
	}
	want := map[int]string{10: "[ds2] vm1_1/vm1_1.vmx", 12: "[ds1] vm2/vm2.vmx", 13: "[ds1] vm3/vm3.vmx", 2: "[ds2] vm4/vm4.vmx", 6: "[ds2] vm7/vm7.vmx"}
	if len(vms) != len(want) {
		t.Fatalf("got %d VMs: %+v", len(vms), vms)
	}
	for _, v := range vms {
		if want[v.ID] != v.VMXPath {
			t.Fatalf("VM %d: %q", v.ID, v.VMXPath)
		}
	}

	ds, e := ParseDatastores(filesystems70)
	if e != nil {
		t.Fatal(e)
	}
	usable := 0
	for _, d := range ds {
		if d.Usable() {
			usable++
		}
	}
	// The system volumes (VFFS, vfat) are listed but never offered.
	if len(ds) != 4 || usable != 2 || ds[1].Name != "ds2" || ds[1].Free != 1000291172352 {
		t.Fatalf("datastores: %+v", ds)
	}

	seq, e := ParseAutostart(autostart70)
	if e != nil || seq[3].Order != 1 || !seq[3].On() || seq[13].Order != 2 {
		t.Fatalf("autostart: %+v %v", seq, e)
	}

	if running, e := ParseToolsRunning(guest70); e != nil || !running {
		t.Fatalf("tools: %t %v", running, e)
	}
	if p, e := ParsePower("Retrieved runtime info\nPowered on\n"); e != nil || p != On {
		t.Fatalf("power: %v %v", p, e)
	}
	if !versionPattern.MatchString("VMware ESXi 7.0.3 build-00000000\nVMware ESXi 7.0 Update 3\n") {
		t.Fatal("ESXi 7.0.3 not recognized")
	}
}
