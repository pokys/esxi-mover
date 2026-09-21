package migration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"esxi-mover/internal/esxi"
	"esxi-mover/internal/vmdk"
	"esxi-mover/internal/vmx"
)

const (
	modeCopy = "COPY"
	modeMove = "MOVE"
)

// Results of a safety check. Any statusBlock makes a report not Ready.
const (
	statusOK      = "OK"
	statusWarning = "WARNING"
	statusBlock   = "BLOCK"
)

// Host is a narrow management boundary. There is intentionally no removal API.
type Host interface {
	Inventory(context.Context) (esxi.Inventory, error)
	VMs(context.Context) ([]esxi.VM, error)
	Datastores(context.Context) ([]esxi.Datastore, error)
	ReadFile(context.Context, string) (string, error)
	Exists(context.Context, string) (bool, error)
	Canonical(context.Context, string) (string, error)
	List(context.Context, string) ([]string, error)
	Size(context.Context, string) (int64, error)
	Allocated(context.Context, string) (int64, error)
	Power(context.Context, int) (esxi.Power, error)
	Snapshot(context.Context, int) (string, error)
	Shutdown(context.Context, int) error
	ForceOff(context.Context, int) error
	VerifyChain(context.Context, string) error
	Acquire(context.Context, string, string) error
	Finish(context.Context, string) error
	CreateTarget(context.Context, string) error
	WriteTarget(context.Context, string, string, []byte) error
	StartClone(context.Context, string, int, int, string, string, bool) error
	CloneStatus(context.Context, int) (esxi.CloneStatus, error)
	StopClone(context.Context, int) error
	Unregister(context.Context, int) error
	Register(context.Context, string) (int, error)
	PowerOn(context.Context, int) error
	Message(context.Context, int) (string, error)
	Answer(context.Context, int, string, string) error
	// Live migration only. The merge acts solely on the job's own snapshot.
	CreateSnapshot(context.Context, int, string) error
	ConsolidateOwnSnapshot(context.Context, int, string) error
	CopyToTarget(context.Context, string, string) error
}
type Request struct {
	VMID             int
	TargetUUID, Mode string
	PowerOn          bool
	// Empty means the source folder's own name.
	TargetName string
	// Live keeps the VM running while its disks are copied (experimental).
	Live bool
}
type Check struct{ Name, Status, Detail string }
type Disk struct {
	Key, Source, Target, Extent string
	Provisioned, Allocated      int64
	AllocationKnown, Thin       bool
	Descriptor                  vmdk.Descriptor `json:"-"`
}
type ConfigFile struct{ Key, Source, Name string }
type Report struct {
	ID                                                                       string
	Request                                                                  Request
	VM                                                                       esxi.VM
	SourceVMX, SourceDir, SourceDatastore, TargetDir, TargetVMX, Fingerprint string
	Power                                                                    esxi.Power
	Disks                                                                    []Disk
	ConfigFiles                                                              []ConfigFile
	TargetFree, Required, Provisioned, Allocated                             int64
	Ready                                                                    bool
	Checks                                                                   []Check
	Config                                                                   vmx.Config `json:"-"`
	TargetConfig                                                             vmx.Config `json:"-"`
}

func (r *Report) check(name, status, detail string) {
	r.Checks = append(r.Checks, Check{name, status, detail})
	if status == statusBlock {
		r.Ready = false
	}
}
func NewID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}

type Options struct {
	ShutdownTimeout, PollInterval, DisconnectTimeout, CloneTimeout, PowerOnTimeout time.Duration
	ApplianceUUID                                                                  string
}

func DefaultOptions() Options {
	return Options{
		ShutdownTimeout:   5 * time.Minute,
		PollInterval:      3 * time.Second,
		DisconnectTimeout: 5 * time.Minute,
		CloneTimeout:      7 * 24 * time.Hour,
		PowerOnTimeout:    2 * time.Minute,
	}
}
func validateRequest(r Request) error {
	if r.VMID <= 0 || r.TargetUUID == "" || (r.Mode != modeCopy && r.Mode != modeMove) || (r.Mode == modeCopy && r.PowerOn) || (r.Live && r.Mode == modeMove && !r.PowerOn) {
		return fmt.Errorf("invalid migration options")
	}
	return nil
}
