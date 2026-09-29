package esxi

import (
	"context"
	"testing"
)

// Shape of esxcli vm process list as documented; not yet captured from a
// real host. Only the Config File lines are read.
const processList = `web
   World ID: 2101
   Process ID: 0
   VMX Cartel ID: 2100
   UUID: 56 4d 00 00 00 00 00 00-00 00 00 00 00 00 00 01
   Display Name: web
   Config File: /vmfs/volumes/uuid-a/web/web.vmx

db
   World ID: 2201
   Process ID: 0
   VMX Cartel ID: 2200
   UUID: 56 4d 00 00 00 00 00 00-00 00 00 00 00 00 00 02
   Display Name: db
   Config File: /vmfs/volumes/store-b/db/db.vmx
`

func TestMarkRunning(t *testing.T) {
	if got := ParseRunningVMX(processList); len(got) != 2 || got[0] != "/vmfs/volumes/uuid-a/web/web.vmx" {
		t.Fatalf("parsed %v", got)
	}
	ds := []Datastore{{Name: "store-a", UUID: "uuid-a"}, {Name: "store-b", UUID: "uuid-b"}}
	vms := []VM{
		{ID: 1, VMXPath: "[store-a] web/web.vmx"},
		{ID: 2, VMXPath: "[store-b] db/db.vmx"}, // listed by datastore name, not UUID
		{ID: 3, VMXPath: "[store-a] old/old.vmx"},
	}
	c := NewClient(&FakeExecutor{RunFunc: func(_ context.Context, cmd Command) (Result, error) {
		if cmd.Script != Argv("esxcli", "vm", "process", "list") {
			t.Fatalf("unexpected command %q", cmd.Script)
		}
		return Result{Stdout: processList}, nil
	}})
	c.MarkRunning(context.Background(), vms, ds)
	for i, want := range []string{"on", "on", "off"} {
		if vms[i].Power != want {
			t.Fatalf("VM %d: %q, want %q", vms[i].ID, vms[i].Power, want)
		}
	}
}
