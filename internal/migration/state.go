package migration

import (
	"errors"
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
	Live, CanStop                                                    bool
	SourceRegistration, TargetRegistration, SourcePower, TargetPower string
	SourceVMX, TargetVMX                                             string
}
type Job struct {
	mu      sync.Mutex
	state   State
	plan    Report
	actions chan string
	stop    bool
	// final is set once the job has passed the point where a stop is honoured.
	final bool
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
	phaseUnknown          = "unknown"
	phaseRolledBack       = "rolled_back"
	phaseSnapshot         = "snapshot"
	phaseCutover          = "cutover"
	phaseConsolidating    = "consolidating"
	phaseRestoring        = "restoring"
	phaseStopped          = "stopped"
)

func NewJob(r Report) *Job {
	return &Job{state: State{ID: r.ID, Phase: phaseQueued, Mode: r.Request.Mode, Live: r.Request.Live, Message: "Waiting for final preflight", Started: time.Now(), Updated: time.Now(), DiskCount: len(r.Disks), SourceFilesPreserved: true, SourceRegistration: "registered", TargetRegistration: "not registered", SourcePower: string(r.Power), TargetPower: "not running (unregistered)", SourceVMX: r.SourceVMX, TargetVMX: r.TargetVMX}, plan: r, actions: make(chan string, 1)}
}
func (j *Job) Snapshot() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.state
	s.CanStop = !s.Complete && !j.stop && !j.final && stoppable(s.Phase)
	return s
}

var errStopped = errors.New("stopped by the operator")

// stoppable lists the phases in which a stop is honoured: everything before
// the first step that cannot simply be abandoned, such as registration, the
// live cutover or merging a snapshot.
func stoppable(phase string) bool {
	switch phase {
	case phaseQueued, phasePreflight, phaseShutdown, phaseAwaitingShutdown, phaseSnapshot, phaseCloning, phaseReconnecting, phaseVerifyingDisk:
		return true
	}
	return false
}
func (j *Job) requestStop(confirmed bool) error {
	if !confirmed {
		return fmt.Errorf("explicit stop confirmation is required")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state.Complete || j.final || !stoppable(j.state.Phase) {
		return fmt.Errorf("the migration is past the point where it can be stopped")
	}
	j.stop = true
	j.state.Message = "Stopping at the next safe point"
	select {
	case j.actions <- "stop":
	default:
	}
	return nil
}
func (j *Job) stopRequested() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stop
}

// proceed enters a phase past which a stop is no longer honoured. A stop
// requested before it wins, so an accepted stop is never silently ignored.
func (j *Job) proceed(p, m string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.stop {
		return errStopped
	}
	j.final = true
	j.state.Phase = p
	j.state.Message = m
	j.state.Updated = time.Now()
	return nil
}
func (j *Job) update(fn func(*State)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	fn(&j.state)
	j.state.Updated = time.Now()
}
func (j *Job) phase(p, m string) { j.update(func(s *State) { s.Phase = p; s.Message = m }) }
func (j *Job) Control(action string, confirmed bool) error {
	if action == "stop" {
		return j.requestStop(confirmed)
	}
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
