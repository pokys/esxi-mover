package esxi

import "testing"

// Shape of get_autostartseq as a real ESXi 6.5 host prints it, including an
// entry whose indentation is off by one.
const autostartSeq = `(vim.host.AutoStartManager.AutoPowerInfo) [
   (vim.host.AutoStartManager.AutoPowerInfo) {
      key = 'vim.VirtualMachine:11',
      startOrder = 3,
      startDelay = -1,
      waitForHeartbeat = "systemDefault",
      startAction = "powerOn",
      stopDelay = -1,
      stopAction = "systemDefault"
   },
   (vim.host.AutoStartManager.AutoPowerInfo) {
      key = 'vim.VirtualMachine:12',
      startOrder = 4,
      startDelay = -1,
     waitForHeartbeat = "systemDefault",
      startAction = "none",
      stopDelay = -1,
      stopAction = "systemDefault"
   }
]
`

func TestParseAutostart(t *testing.T) {
	got, e := ParseAutostart(autostartSeq)
	if e != nil {
		t.Fatal(e)
	}
	if a := got[11]; a.Order != 3 || !a.On() {
		t.Fatalf("VM 11: %+v", a)
	}
	if a := got[12]; a.Order != 4 || a.On() {
		t.Fatalf("VM 12: %+v", a)
	}
	if _, ok := got[7]; ok || len(got) != 2 {
		t.Fatalf("unexpected entries: %+v", got)
	}
	if empty, e := ParseAutostart("(vim.host.AutoStartManager.AutoPowerInfo) [\n]\n"); e != nil || len(empty) != 0 {
		t.Fatalf("an empty sequence: %v %v", empty, e)
	}
	for name, bad := range map[string]string{
		"no key":     "(vim.host.AutoStartManager.AutoPowerInfo) {\n startOrder = 1,\n}\n",
		"duplicate":  autostartSeq + autostartSeq,
		"unfinished": "(vim.host.AutoStartManager.AutoPowerInfo) {\n key = 'vim.VirtualMachine:1',\n",
	} {
		if _, e := ParseAutostart(bad); e == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
