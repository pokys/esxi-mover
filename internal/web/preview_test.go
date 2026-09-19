package web

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"testing"
	"time"

	"esxi-mover/internal/esxi"
	"esxi-mover/internal/migration"
)

// Opt-in, loopback-only visual fixture; no SSH, credentials or ESXi side effects.
// Run MOVER_VISUAL_FIXTURE=1 go test ./internal/web -run TestVisualFixture -timeout 10m.
func TestVisualFixture(t *testing.T) {
	if os.Getenv("MOVER_VISUAL_FIXTURE") != "1" {
		t.Skip("opt-in browser fixture")
	}
	mux := http.NewServeMux()
	sub, _ := fs.Sub(assets, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) { problem(w, 401, "Preview login") })
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]string{"csrf": "visual-fixture"})
	})
	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]string{"fingerprint": "SHA256:SYNTHETIC-VISUAL-FIXTURE-NO-SSH-CONNECTION", "address": "lab-esxi.example:22"})
	})
	mux.HandleFunc("/api/connect", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, esxi.Inventory{Capabilities: esxi.Capabilities{Version: "VMware ESXi 6.7.0 · SYNTHETIC VISUAL FIXTURE", Supported: true}, VMs: []esxi.VM{{ID: 7, Name: "LAB-SERVER", Datastore: "VMFS-A"}}, Datastores: []esxi.Datastore{{Name: "VMFS-B", UUID: "target", Type: "VMFS-6", Mounted: true, Free: 820 << 30}}})
	})
	mux.HandleFunc("/api/analyze", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, migration.Report{ID: "visual-fixture", Ready: true, SourceDatastore: "VMFS-A", Power: esxi.On, TargetFree: 820 << 30, Required: 462 << 30, TargetVMX: "/vmfs/volumes/VMFS-B/esxi-mover-7-example/LAB-SERVER.vmx", Disks: []migration.Disk{{Source: "SYSTEM.vmdk", Provisioned: 100 << 30, Allocated: 42 << 30, AllocationKnown: true, Thin: true}, {Source: "DATA.vmdk", Provisioned: 300 << 30, Allocated: 180 << 30, AllocationKnown: true, Thin: true}}, Checks: []migration.Check{{Name: "Snapshot Manager", Status: "OK", Detail: "No snapshots reported"}, {Name: "Active VMDK parent chains", Status: "OK", Detail: "Both disks are standalone"}, {Name: "Orphan delta files", Status: "OK", Detail: "No snapshot artifacts found"}, {Name: "Shared disk inventory", Status: "OK", Detail: "No other VM references the disks"}, {Name: "External ISO", Status: "WARNING", Detail: "[VMFS-A] ISO/install.iso remains on its existing datastore"}, {Name: "Free space", Status: "OK", Detail: "Provisioned capacity + 15% + 1 GiB"}}})
	})
	started := time.Now()
	state := func() migration.State {
		return migration.State{ID: "visual-fixture", Phase: "completed", Message: "Synthetic COPY completed. No ESXi host was contacted.", Mode: "COPY", Progress: 100, DiskIndex: 2, DiskCount: 2, Started: started, Updated: time.Now(), Complete: true, TargetVerified: true, SourceFilesPreserved: true, SourceRegistration: "registered", TargetRegistration: "not registered", SourcePower: "Powered off", TargetPower: "not running (unregistered)", SourceVMX: "/vmfs/volumes/VMFS-A/LAB-SERVER/LAB-SERVER.vmx", TargetVMX: "/vmfs/volumes/VMFS-B/esxi-mover-7-example/LAB-SERVER.vmx", CurrentDisk: "DATA.vmdk", TechnicalLog: "SYNTHETIC FIXTURE\nClone: 100% done."}
	}
	mux.HandleFunc("/api/start", func(w http.ResponseWriter, r *http.Request) { respond(w, 202, state()) })
	mux.HandleFunc("/api/job", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, state()) })
	mux.HandleFunc("/api/log", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, []esxi.Event{{Time: time.Now(), Category: "synthetic-visual-fixture", DurationMS: 5, ExitCode: 0}})
	})
	fmt.Println("Synthetic browser fixture: http://127.0.0.1:8844 (no live SSH)")
	t.Fatal(http.ListenAndServe("127.0.0.1:8844", mux))
}
