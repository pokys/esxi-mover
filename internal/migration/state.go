package migration

import (
	"fmt"
	"sync"
	"time"
)

type State struct {
	ID, Phase, Message, Error, CurrentDisk, TechnicalLog             string
	Mode                                                             string
	Progress, DiskIndex, DiskCount, TargetVMID                       int
	Started, Updated, DiskStarted                                    time.Time
	DiskBytes                                                        int64
	Complete, TargetVerified, SourceFilesPreserved, CanRollback      bool
	Live                                                             bool
	SourceRegistration, TargetRegistration, SourcePower, TargetPower string
	SourceVMX, TargetVMX                                             string
}
type Job struct {
	mu      sync.Mutex
	state   State
	plan    Report
	actions chan string
}

// Job phases, as the web UI receives them.
const (
	phaseQueued           = "queued"
	phasePreflight        = "preflight"
	phaseShutdown         = "shutdown"
	phaseAwaitingShutdown = "awaiting_shutdown"
	phaseCloning          = "cloning"
	phaseReconnecting     = "reconnecting"
	phaseVerifyingDisk    = "verifying_disk"
	phaseVerifyingConfig  = "verifying_config"
	phaseCommit           = "commit"
	phasePowerOn          = "power_on"
	phaseCompleted        = "completed"
	phaseFailed           = "failed"
	phaseRolledBack       = "rolled_back"
	phaseSnapshot         = "snapshot"
	phaseCutover          = "cutover"
	phaseConsolidating    = "consolidating"
	phaseRestoring        = "restoring"
)

func NewJob(r Report) *Job {
	return &Job{state: State{ID: r.ID, Phase: phaseQueued, Mode: r.Request.Mode, Live: r.Request.Live, Message: "Waiting for final preflight", Started: time.Now(), Updated: time.Now(), DiskCount: len(r.Disks), SourceFilesPreserved: true, SourceRegistration: "registered", TargetRegistration: "not registered", SourcePower: string(r.Power), TargetPower: "not running (unregistered)", SourceVMX: r.SourceVMX, TargetVMX: r.TargetVMX}, plan: r, actions: make(chan string, 1)}
}
func (j *Job) Snapshot() State { j.mu.Lock(); defer j.mu.Unlock(); return j.state }
func (j *Job) update(fn func(*State)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	fn(&j.state)
	j.state.Updated = time.Now()
}
func (j *Job) phase(p, m string) { j.update(func(s *State) { s.Phase = p; s.Message = m }) }
func (j *Job) Control(action string, confirmed bool) error {
	if j.Snapshot().Phase != phaseAwaitingShutdown {
		return fmt.Errorf("job is not waiting for shutdown")
	}
	if action != "wait" && action != "manual" && action != "force" {
		return fmt.Errorf("unknown action")
	}
	if action == "force" && !confirmed {
		return fmt.Errorf("explicit force power-off confirmation is required")
	}
	select {
	case j.actions <- action:
		return nil
	default:
		return fmt.Errorf("an action is already pending")
	}
}
