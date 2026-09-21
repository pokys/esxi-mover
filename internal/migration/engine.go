package migration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"esxi-mover/internal/esxi"
)

type Engine struct {
	Host    Host
	Options Options
}

func (e *Engine) analyzer() Analyzer {
	return Analyzer{Host: e.Host, ApplianceUUID: e.Options.ApplianceUUID}
}
func (e *Engine) Run(ctx context.Context, j *Job) {
	if j.plan.Request.Live {
		e.runLive(ctx, j)
		return
	}
	if err := e.run(ctx, j); err != nil {
		if errors.Is(err, errStopped) {
			e.stopped(ctx, j)
			return
		}
		markFailed(j, err)
	}
}

// stopped ends a cold migration the operator stopped. By then no clone is
// running, so the lock is released. The source is left as it is: a cold
// migration never starts it on its own.
func (e *Engine) stopped(ctx context.Context, j *Job) {
	finishErr := e.Host.Finish(ctx, j.plan.ID)
	j.update(func(s *State) {
		s.Phase = phaseStopped
		s.Complete = true
		s.Message = "Stopped by you. The source is still registered (" + s.SourcePower + "); if it was shut down, start it in Host Client. Nothing was registered on the target; its partial folder is kept."
		if finishErr != nil {
			s.Error = "The operation lock could not be archived: " + finishErr.Error()
		}
	})
}
func (e *Engine) run(ctx context.Context, j *Job) error {
	r := j.plan
	if !r.Ready {
		return fmt.Errorf("analysis was blocked")
	}
	j.phase(phasePreflight, "Repeating all critical safety checks")
	fresh, err := e.analyzer().inspect(ctx, r.Request, r.ID, progress{})
	if err != nil {
		return err
	}
	if err = matchPlan(r, fresh); err != nil {
		return err
	}
	if err = e.Host.Acquire(ctx, r.ID, "source="+r.SourceVMX+"\ntarget="+r.TargetVMX+"\nmode="+r.Request.Mode+"\n"); err != nil {
		return fmt.Errorf("cannot acquire host operation lock: %w", err)
	}
	fresh, err = e.analyzer().inspect(ctx, r.Request, r.ID, progress{ownLock: true})
	if err != nil {
		return err
	}
	if err = matchPlan(r, fresh); err != nil {
		return err
	}
	if err = e.shutdown(ctx, j, fresh, func() error {
		fresh, err := e.analyzer().inspect(ctx, r.Request, r.ID, progress{ownLock: true})
		if err != nil {
			return err
		}
		return matchPlan(r, fresh)
	}); err != nil {
		return err
	}
	// Re-read VMX and every snapshot signal after shutdown, before any clone.
	cold, err := e.guard(ctx, r, false, 0)
	if err != nil {
		return err
	}
	r = cold
	j.plan = r
	if err = e.Host.CreateTarget(ctx, r.TargetDir); err != nil {
		return err
	}
	consumed := int64(0)
	for i, d := range r.Disks {
		if j.stopRequested() {
			return errStopped
		}
		if _, err = e.guard(ctx, r, true, consumed); err != nil {
			return err
		}
		j.update(func(s *State) {
			s.Phase = phaseCloning
			s.Message = "Cloning disk sequentially with vmkfstools"
			s.DiskIndex = i + 1
			s.CurrentDisk = d.Source
			s.Progress = 0
			s.DiskStarted = time.Now()
			s.DiskBytes = d.Provisioned
		})
		// Last local OFF check; the detached worker checks it again on ESXi.
		if err = e.requireOff(ctx, r.VM.ID); err != nil {
			return err
		}
		startErr := e.Host.StartClone(ctx, r.ID, i, r.VM.ID, d.Source, d.Target, true)
		if startErr != nil {
			j.phase(phaseReconnecting, "Clone launch result is unknown; inspecting metadata without relaunching")
		}
		if err = e.waitClone(ctx, j, i, startErr); err != nil {
			return err
		}
		if err = e.requireOff(ctx, r.VM.ID); err != nil {
			return err
		}
		j.phase(phaseVerifyingDisk, "Verifying target descriptor, extent, capacity, thin format and chain")
		if err = verifyDisk(ctx, e.Host, d); err != nil {
			return fmt.Errorf("target disk verification failed: %w", err)
		}
		consumed += d.Allocated
	}
	if _, err = e.guard(ctx, r, true, consumed); err != nil {
		return err
	}
	if err = j.proceed(phaseVerifyingConfig, "Copying only selected configuration files and verifying target VMX"); err != nil {
		return err
	}
	if err = copyConfig(ctx, e.Host, r, r.TargetConfig, diskNames(r.Disks, nil)); err != nil {
		return err
	}
	// Verify all targets together once more before the commit boundary.
	for _, d := range r.Disks {
		if err = verifyDisk(ctx, e.Host, d); err != nil {
			return err
		}
	}
	if _, err = e.guard(ctx, r, true, consumed); err != nil {
		return err
	}
	j.update(func(s *State) { s.TargetVerified = true })
	if r.Request.Mode == modeMove {
		if err = e.commit(ctx, j, r); err != nil {
			return err
		}
		if r.Request.PowerOn {
			if err = e.powerOn(ctx, j); err != nil {
				return err
			}
		}
	}
	if err = e.Host.Finish(ctx, r.ID); err != nil {
		return fmt.Errorf("migration finished but transient lock archive failed; manual review required: %w", err)
	}
	j.update(func(s *State) {
		s.Phase = phaseCompleted
		s.Message = "Completed. No source VM files were deleted."
		s.Complete = true
		s.CanRollback = false
		s.SourcePower = "Powered off"
	})
	return nil
}
func matchPlan(before, after Report) error {
	if !after.Ready {
		parts := []string{}
		for _, c := range after.Checks {
			if c.Status == statusBlock {
				parts = append(parts, c.Name+": "+c.Detail)
			}
		}
		return fmt.Errorf("preflight blocked: %s", strings.Join(parts, "; "))
	}
	if before.Fingerprint != after.Fingerprint || before.SourceVMX != after.SourceVMX || before.TargetVMX != after.TargetVMX {
		return fmt.Errorf("VM identity, configuration or disk dependency changed after Analyze; analyze again")
	}
	return nil
}
func (e *Engine) guard(ctx context.Context, r Report, created bool, consumed int64) (Report, error) {
	fresh, err := e.analyzer().inspect(ctx, r.Request, r.ID, progress{ownLock: true, targetCreated: created, consumed: consumed})
	if err != nil {
		return fresh, err
	}
	if err = matchPlan(r, fresh); err != nil {
		return fresh, err
	}
	if fresh.Power != esxi.Off {
		return fresh, fmt.Errorf("source VM is not powered off; no clone or commit is permitted")
	}
	return fresh, nil
}
func (e *Engine) requireOff(ctx context.Context, id int) error {
	p, err := e.Host.Power(ctx, id)
	if err != nil {
		return err
	}
	if p != esxi.Off {
		return fmt.Errorf("source VM must be Powered off")
	}
	return nil
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + " ..."
}

func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
// shutdown powers the source off gracefully. revalidate runs before a forced
// power-off is honoured, so the VM is checked again at the last moment.
func (e *Engine) shutdown(ctx context.Context, j *Job, r Report, revalidate func() error) error {
	if r.Power == esxi.Off {
		j.update(func(s *State) { s.SourcePower = "Powered off" })
		return nil
	}
	if r.Power != esxi.On {
		return fmt.Errorf("unsupported source power state")
	}
	j.phase(phaseShutdown, "Requesting a graceful guest shutdown")
	shutdownErr := e.Host.Shutdown(ctx, r.VM.ID)
	deadline := time.Now().Add(e.Options.ShutdownTimeout)
	if shutdownErr != nil {
		deadline = time.Now()
	}
	for {
		if j.stopRequested() {
			return errStopped
		}
		p, err := e.Host.Power(ctx, r.VM.ID)
		if err != nil {
			return err
		}
		if p == esxi.Off {
			j.update(func(s *State) { s.SourcePower = "Powered off" })
			return nil
		}
		if p != esxi.On {
			return fmt.Errorf("unexpected power state during shutdown")
		}
		if time.Now().After(deadline) {
			j.phase(phaseAwaitingShutdown, "Graceful shutdown failed or VMware Tools is unavailable. Wait, shut down manually, or explicitly confirm Force Power Off.")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case action := <-j.actions:
				if action == "stop" {
					return errStopped
				}
				if action == "force" {
					if err := revalidate(); err != nil {
						return err
					}
					if err = e.Host.ForceOff(ctx, r.VM.ID); err != nil {
						return err
					}
				}
				deadline = time.Now().Add(e.Options.ShutdownTimeout)
				j.phase(phaseShutdown, "Waiting for confirmed Powered off state")
			}
		}
		if err = pause(ctx, e.Options.PollInterval); err != nil {
			return err
		}
	}
}
func (e *Engine) waitClone(ctx context.Context, j *Job, index int, startErr error) error {
	deadline := time.Now().Add(e.Options.CloneTimeout)
	lastContact := time.Now()
	startingSince := time.Now()
	stopping := false
	for time.Now().Before(deadline) {
		// A stop ends only this job's recorded clone process; the worker still
		// publishes the exit code, so completion is observed as usual.
		if j.stopRequested() && !stopping && e.Host.StopClone(ctx, index) == nil {
			stopping = true
			j.update(func(s *State) { s.Message = "Stopping the clone" })
		}
		status, err := e.Host.CloneStatus(ctx, index)
		if err != nil {
			if time.Since(lastContact) > e.Options.DisconnectTimeout {
				return fmt.Errorf("cannot determine remote clone outcome; it may still be running; inspect ESXi transient metadata: %w", err)
			}
			j.phase(phaseReconnecting, "SSH unavailable or remote process state uncertain; clone will never be relaunched automatically")
		} else {
			lastContact = time.Now()
			j.update(func(s *State) { s.Phase = phaseCloning; s.Progress = status.Progress; s.TechnicalLog = status.Log })
			if status.Done {
				if j.stopRequested() {
					return errStopped
				}
				if status.ExitCode != 0 {
					return fmt.Errorf("vmkfstools clone exited with code %d; source registration is unchanged", status.ExitCode)
				}
				j.update(func(s *State) { s.Progress = 100 })
				return nil
			}
			if status.Log == "" && time.Since(startingSince) > e.Options.DisconnectTimeout {
				return fmt.Errorf("clone did not produce progress or an exit marker; launch outcome remains unknown (%v)", startErr)
			}
		}
		if err = pause(ctx, e.Options.PollInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("clone observation deadline exceeded; detached worker may still be running")
}

// registrations reconciles uncertain mutation results against canonical VMX paths.
// A transport error is never treated as proof that a registration did not occur.
func (e *Engine) registrations(ctx context.Context, source, target string) (int, int, error) {
	vms, err := e.Host.VMs(ctx)
	if err != nil {
		return 0, 0, err
	}
	ds, err := e.Host.Datastores(ctx)
	if err != nil {
		return 0, 0, err
	}
	src, dst := 0, 0
	for _, v := range vms {
		p, err := esxi.ResolveReference(v.VMXPath, "", ds)
		if err != nil {
			return 0, 0, err
		}
		p, err = e.Host.Canonical(ctx, p)
		if err != nil {
			return 0, 0, err
		}
		if p == source {
			if src != 0 {
				return 0, 0, fmt.Errorf("duplicate source registrations")
			}
			src = v.ID
		}
		if p == target {
			if dst != 0 {
				return 0, 0, fmt.Errorf("duplicate target registrations")
			}
			dst = v.ID
		}
	}
	return src, dst, nil
}
func (e *Engine) commit(ctx context.Context, j *Job, r Report) error {
	if !j.Snapshot().TargetVerified {
		return fmt.Errorf("commit denied: target is not fully verified")
	}
	src, dst, err := e.registrations(ctx, r.SourceVMX, r.TargetVMX)
	if err != nil {
		return err
	}
	if src != r.VM.ID || dst != 0 {
		return fmt.Errorf("registration identity changed before commit")
	}
	if err = e.requireOff(ctx, src); err != nil {
		return err
	}
	j.phase(phaseCommit, "Target complete and verified. Switching VM registration; source files remain in place.")
	j.update(func(s *State) { s.SourceRegistration = "unknown" })
	unregisterErr := e.Host.Unregister(ctx, src)
	src, dst, err = e.registrations(ctx, r.SourceVMX, r.TargetVMX)
	if err != nil {
		return fmt.Errorf("unregister outcome unknown; manual inventory reconciliation required")
	}
	publishRegistrations(j, src, dst)
	if src != 0 {
		j.update(func(s *State) { s.SourceRegistration = "registered" })
		return fmt.Errorf("source remains registered; stopping commit (%v)", unregisterErr)
	}
	j.update(func(s *State) { s.SourceRegistration = "not registered" })
	if dst != 0 {
		return fmt.Errorf("target unexpectedly registered by another actor; manual review required")
	}
	_, registerErr := e.Host.Register(ctx, r.TargetVMX)
	j.update(func(s *State) { s.TargetRegistration = "unknown" })
	src, dst, err = e.registrations(ctx, r.SourceVMX, r.TargetVMX)
	if err != nil {
		return fmt.Errorf("target registration result is unknown; cannot safely auto-register source while target may be registered")
	}
	publishRegistrations(j, src, dst)
	if src != 0 {
		return fmt.Errorf("source was externally registered during commit; manual review required")
	}
	if dst == 0 {
		j.update(func(s *State) { s.TargetRegistration = "not registered"; s.SourceRegistration = "unknown" })
		_, rollbackErr := e.Host.Register(ctx, r.SourceVMX)
		src, dst, reconcileErr := e.registrations(ctx, r.SourceVMX, r.TargetVMX)
		if reconcileErr == nil {
			publishRegistrations(j, src, dst)
		}
		if reconcileErr == nil && src > 0 && dst == 0 {
			j.update(func(s *State) { s.SourceRegistration = "registered" })
			return fmt.Errorf("target registration failed; source registration restored; source remains off (%v)", registerErr)
		}
		return fmt.Errorf("target registration failed and source rollback requires manual review (%v; %v)", registerErr, rollbackErr)
	}
	j.update(func(s *State) {
		s.TargetRegistration = "registered"
		s.TargetVMID = dst
		s.TargetPower = "unknown"
		s.CanRollback = true
	})
	p, err := e.Host.Power(ctx, dst)
	if err != nil {
		return err
	}
	j.update(func(s *State) { s.TargetPower = string(p) })
	if p != esxi.Off {
		return fmt.Errorf("target was unexpectedly powered on; automation stopped")
	}
	return nil
}
func (e *Engine) powerOn(ctx context.Context, j *Job) error {
	s := j.Snapshot()
	r := j.plan
	src, dst, err := e.registrations(ctx, r.SourceVMX, r.TargetVMX)
	if err != nil || src != 0 || dst != s.TargetVMID {
		return fmt.Errorf("cannot confirm source unregistered and target identity before Power On")
	}
	j.phase(phasePowerOn, "Requesting target Power On; source remains unregistered")
	// Some ESXi builds keep this command pending while a VM question is open.
	// Bound the command and inspect actual state/questions even if its reply is lost.
	powerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	powerErr := e.Host.PowerOn(powerCtx, s.TargetVMID)
	cancel()
	deadline := time.Now().Add(e.Options.PowerOnTimeout)
	answered := false
	for time.Now().Before(deadline) {
		p, err := e.Host.Power(ctx, s.TargetVMID)
		if err != nil {
			return err
		}
		j.update(func(s *State) { s.TargetPower = string(p) })
		message, err := e.Host.Message(ctx, s.TargetVMID)
		if err != nil {
			return err
		}
		pending := strings.TrimSpace(message) != "" && strings.TrimSpace(message) != "No message." && strings.TrimSpace(message) != "No message"
		// A VM blocked on a question already reports Powered on while the guest
		// has not started, so an open question outranks the power state.
		if !pending && p == esxi.On {
			j.update(func(s *State) { s.CanRollback = false })
			return nil
		}
		if pending {
			id, choice, err := esxi.MovedAnswer(message)
			if err != nil {
				// The question itself is what makes this answerable by hand.
				return fmt.Errorf("%w; ESXi asked: %s", err, truncate(strings.TrimSpace(message), 400))
			}
			if answered {
				return fmt.Errorf("VM question remained after answering; manual review required")
			}
			if err = e.Host.Answer(ctx, s.TargetVMID, id, choice); err != nil {
				return err
			}
			answered = true
		}
		if err = pause(ctx, e.Options.PollInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("target did not reach Powered on; it remains registered (%v)", powerErr)
}

// Rollback is an explicit registration-only action after a failed MOVE. It never
// powers either VM on, and refuses to unregister a running/unknown target.
func (e *Engine) Rollback(ctx context.Context, j *Job) error {
	s := j.Snapshot()
	if !s.Complete || !s.CanRollback || s.Phase != phaseFailed {
		return fmt.Errorf("registration rollback is unavailable")
	}
	r := j.plan
	src, dst, err := e.registrations(ctx, r.SourceVMX, r.TargetVMX)
	if err != nil || src != 0 || dst != s.TargetVMID {
		return fmt.Errorf("rollback inventory is ambiguous")
	}
	if err = e.requireOff(ctx, dst); err != nil {
		return fmt.Errorf("rollback requires target Powered off")
	}
	message, messageErr := e.Host.Message(ctx, dst)
	if messageErr != nil || (strings.TrimSpace(message) != "" && strings.TrimSpace(message) != "No message." && strings.TrimSpace(message) != "No message") {
		return fmt.Errorf("resolve pending target questions/tasks in Host Client before registration rollback")
	}
	j.update(func(s *State) { s.TargetRegistration = "unknown"; s.CanRollback = false })
	_ = e.Host.Unregister(ctx, dst)
	src, dst, err = e.registrations(ctx, r.SourceVMX, r.TargetVMX)
	if err == nil {
		publishRegistrations(j, src, dst)
	}
	if err != nil || src != 0 || dst != 0 {
		return fmt.Errorf("target unregister outcome is not safe for rollback")
	}
	j.update(func(s *State) { s.TargetRegistration = "not registered"; s.SourceRegistration = "unknown" })
	_, registerErr := e.Host.Register(ctx, r.SourceVMX)
	src, dst, err = e.registrations(ctx, r.SourceVMX, r.TargetVMX)
	if err == nil {
		publishRegistrations(j, src, dst)
	}
	if err != nil || src == 0 || dst != 0 {
		return fmt.Errorf("source re-registration needs manual review (%v)", registerErr)
	}
	j.update(func(s *State) {
		s.SourceRegistration = "registered"
		s.TargetPower = "Powered off"
		s.SourcePower = "Powered off"
	})
	if err = e.Host.Finish(ctx, r.ID); err != nil {
		return err
	}
	j.update(func(s *State) {
		s.Phase = phaseRolledBack
		s.Error = ""
		s.Message = "Source registration restored. Both copies remain off; all files are preserved."
	})
	return nil
}

func publishRegistrations(j *Job, source, target int) {
	j.update(func(s *State) {
		s.SourceRegistration = "not registered"
		if source > 0 {
			s.SourceRegistration = "registered"
		}
		s.TargetRegistration = "not registered"
		if target > 0 {
			s.TargetRegistration = "registered"
			s.TargetVMID = target
		}
	})
}
