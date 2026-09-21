package migration

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"esxi-mover/internal/esxi"
	"esxi-mover/internal/vmdk"
	"esxi-mover/internal/vmx"
)

// Live migration (experimental).
//
// The VM keeps running while its base disks are cloned behind a snapshot the
// job takes itself. A COPY then merges that snapshot back into the source and
// is done. A MOVE shuts the VM down, copies only what the guest wrote in the
// meantime (the snapshot delta), switches registration and powers the target
// on, where the snapshot is merged while the VM runs.
//
// Until the target runs, every failure restores the source as it was:
// registered, running, without the snapshot. Nothing is ever deleted; what was
// written to the target folder stays there for inspection.

func liveSnapshot(id string) string { return "esxi-mover-" + id[:8] }

// errRestored is a failure after which the source was put back as it was.
type errRestored struct{ cause error }

func (e errRestored) Error() string { return e.cause.Error() }

// delta is the snapshot delta a running disk writes to, as source paths.
type delta struct{ descriptor, extent string }

func markFailed(j *Job, err error) {
	j.update(func(s *State) {
		s.Phase = phaseFailed
		s.Error = err.Error()
		s.Message = "Stopped. Source files are preserved. Review the exact registration state before taking action."
		s.Complete = true
	})
}

func (e *Engine) runLive(ctx context.Context, j *Job) {
	err := e.live(ctx, j)
	var restored errRestored
	switch {
	case err == nil:
	case errors.As(err, &restored):
		j.update(func(s *State) {
			s.Phase = phaseRolledBack
			s.Error = restored.cause.Error()
			s.Message = "Nothing changed: the source runs as before, without the temporary snapshot. Files written to the target folder are kept for inspection."
			s.Complete = true
			s.CanRollback = false
		})
	default:
		markFailed(j, err)
	}
}

func (e *Engine) live(ctx context.Context, j *Job) (err error) {
	r := j.plan
	if !r.Ready {
		return fmt.Errorf("analysis was blocked")
	}
	j.phase(phasePreflight, "Repeating all critical safety checks")
	check := func(done progress) (Report, error) {
		fresh, err := e.analyzer().inspect(ctx, r.Request, r.ID, done)
		if err != nil {
			return fresh, err
		}
		if err = matchPlan(r, fresh); err != nil {
			return fresh, err
		}
		if fresh.Power != esxi.On {
			return fresh, fmt.Errorf("live migration needs the source running; analyze again for a cold migration")
		}
		return fresh, nil
	}
	if _, err = check(progress{}); err != nil {
		return err
	}
	if err = e.Host.Acquire(ctx, r.ID, "source="+r.SourceVMX+"\ntarget="+r.TargetVMX+"\nmode=live-"+r.Request.Mode+"\n"); err != nil {
		return fmt.Errorf("cannot acquire host operation lock: %w", err)
	}
	fresh, err := check(progress{ownLock: true})
	if err != nil {
		return err
	}
	name := liveSnapshot(r.ID)
	j.phase(phaseSnapshot, "Taking a temporary snapshot; the VM keeps running")
	snapErr := e.Host.CreateSnapshot(ctx, r.VM.ID, name)
	// From here the snapshot may exist, even when the reply was lost. Until the
	// target runs, every failure merges it again and restarts the source.
	targetRuns := false
	defer func() {
		if err == nil || targetRuns {
			return
		}
		j.phase(phaseRestoring, "Restoring the source: merging the temporary snapshot and starting the VM again")
		if rerr := e.restoreSource(ctx, j, r, name); rerr != nil {
			err = fmt.Errorf("%v; restoring the source needs manual review: %v", err, rerr)
			return
		}
		err = errRestored{err}
	}()
	if snapErr != nil {
		return fmt.Errorf("the snapshot could not be taken: %w", snapErr)
	}
	deltas, err := e.liveDeltas(ctx, r, name)
	if err != nil {
		return err
	}
	if err = e.Host.CreateTarget(ctx, r.TargetDir); err != nil {
		return err
	}
	for i, d := range r.Disks {
		if err = e.liveGuard(ctx, r, name, deltas); err != nil {
			return err
		}
		j.update(func(s *State) {
			s.Phase = phaseCloning
			s.Message = "Cloning the base disk while the VM keeps running"
			s.DiskIndex = i + 1
			s.CurrentDisk = d.Source
			s.Progress = 0
			s.DiskStarted = time.Now()
			s.DiskBytes = d.Provisioned
		})
		startErr := e.Host.StartClone(ctx, r.ID, i, r.VM.ID, d.Source, d.Target, false)
		if startErr != nil {
			j.phase(phaseReconnecting, "Clone launch result is unknown; inspecting metadata without relaunching")
		}
		if err = e.waitClone(ctx, j, i, startErr); err != nil {
			return err
		}
		j.phase(phaseVerifyingDisk, "Verifying the cloned base disk")
		if err = verifyDisk(ctx, e.Host, d); err != nil {
			return fmt.Errorf("target disk verification failed: %w", err)
		}
		if err = sameCID(ctx, e.Host, d); err != nil {
			return err
		}
	}
	if r.Request.Mode == modeCopy {
		return e.liveCopy(ctx, j, r, name)
	}

	// Cutover: from the graceful shutdown until the target runs, the VM is down.
	if err = e.liveGuard(ctx, r, name, deltas); err != nil {
		return err
	}
	j.phase(phaseCutover, "Shutting down the source to copy what changed during the clone")
	if err = e.shutdown(ctx, j, fresh, func() error { return e.liveGuard(ctx, r, name, deltas) }); err != nil {
		return err
	}
	if err = e.liveGuard(ctx, r, name, deltas); err != nil {
		return err
	}
	meta, err := e.liveMetadata(ctx, r, name)
	if err != nil {
		return err
	}
	j.phase(phaseCutover, "Copying the changes and the snapshot metadata to the target")
	names := map[string]string{}
	for _, d := range r.Disks {
		dl := deltas[d.Key]
		for _, f := range []string{dl.descriptor, dl.extent} {
			if err = e.Host.CopyToTarget(ctx, f, r.TargetDir); err != nil {
				return err
			}
		}
		names[d.Key] = path.Base(dl.descriptor)
	}
	for _, f := range meta {
		if err = e.Host.CopyToTarget(ctx, f, r.TargetDir); err != nil {
			return err
		}
	}
	j.phase(phaseVerifyingConfig, "Writing the target configuration and verifying disks, changes and metadata")
	config := r.TargetConfig.Clone()
	for k, v := range names {
		config[k] = v
	}
	if err = copyConfig(ctx, e.Host, r, config, diskNames(r.Disks, names)); err != nil {
		return err
	}
	for _, d := range r.Disks {
		if err = verifyDisk(ctx, e.Host, d); err != nil {
			return err
		}
		if err = e.verifyDelta(ctx, r, deltas[d.Key]); err != nil {
			return err
		}
	}
	if err = e.verifyCopies(ctx, r, meta); err != nil {
		return err
	}
	j.update(func(s *State) { s.TargetVerified = true })
	if err = e.commit(ctx, j, r); err != nil {
		return err
	}
	if err = e.powerOn(ctx, j); err != nil {
		return err
	}
	targetRuns = true

	// The migration is done. Merging the snapshot on the running target is
	// housekeeping: when it fails the VM simply keeps running on the snapshot.
	dst := j.Snapshot().TargetVMID
	j.phase(phaseConsolidating, "Merging the temporary snapshot on the target; the VM is running")
	warning := ""
	if cerr := e.Host.ConsolidateOwnSnapshot(ctx, dst, name); cerr != nil {
		warning = cerr.Error()
	} else if verr := e.merged(ctx, dst, r.TargetVMX, diskNames(r.Disks, nil)); verr != nil {
		warning = verr.Error()
	}
	if err = e.Host.Finish(ctx, r.ID); err != nil {
		return fmt.Errorf("migration finished but transient lock archive failed; manual review required: %w", err)
	}
	j.update(func(s *State) {
		s.Phase = phaseCompleted
		s.Message = "Completed. The VM runs on the target; it was down only while the changes were copied. No source VM files were deleted."
		s.Complete = true
		s.CanRollback = false
		s.SourcePower = "Powered off"
		if warning != "" {
			s.Error = "The VM runs on the target, but its temporary snapshot " + name + " could not be merged (" + warning + "). Consolidate it in Host Client."
		}
	})
	return nil
}

// liveCopy finishes a live COPY: the target holds the base disks as they were
// at the snapshot, and the source gets its snapshot merged back.
func (e *Engine) liveCopy(ctx context.Context, j *Job, r Report, name string) error {
	j.phase(phaseVerifyingConfig, "Writing and verifying the target configuration")
	if err := copyConfig(ctx, e.Host, r, r.TargetConfig, diskNames(r.Disks, nil)); err != nil {
		return err
	}
	for _, d := range r.Disks {
		if err := verifyDisk(ctx, e.Host, d); err != nil {
			return err
		}
	}
	j.update(func(s *State) { s.TargetVerified = true })
	j.phase(phaseConsolidating, "Merging the temporary snapshot back into the source; the VM keeps running")
	if err := e.Host.ConsolidateOwnSnapshot(ctx, r.VM.ID, name); err != nil {
		return err
	}
	if err := e.merged(ctx, r.VM.ID, r.SourceVMX, sourceNames(r)); err != nil {
		return err
	}
	if err := e.Host.Finish(ctx, r.ID); err != nil {
		return fmt.Errorf("migration finished but transient lock archive failed; manual review required: %w", err)
	}
	j.update(func(s *State) {
		s.Phase = phaseCompleted
		s.Message = "Completed. The copy holds the disks as they were when the snapshot was taken, like after a power cut. The source kept running."
		s.Complete = true
		s.SourcePower = "Powered on"
	})
	return nil
}

// liveDeltas confirms the VM runs on exactly the job's snapshot and finds, for
// every disk, the delta it now writes to. The delta must name the source base
// disk as its parent by file and by CID.
func (e *Engine) liveDeltas(ctx context.Context, r Report, name string) (map[string]delta, error) {
	if err := e.ownSnapshotOnly(ctx, r.VM.ID, name); err != nil {
		return nil, err
	}
	raw, err := e.Host.ReadFile(ctx, r.SourceVMX)
	if err != nil {
		return nil, err
	}
	cfg, err := vmx.Parse(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]delta{}
	for _, d := range r.Disks {
		file := cfg[d.Key]
		if path.IsAbs(file) && path.Dir(file) == r.SourceDir {
			file = path.Base(file)
		}
		if file == "" || file != path.Base(file) || file == path.Base(d.Source) || esxi.ValidPath(file) != nil {
			return nil, fmt.Errorf("the VM does not run on a snapshot delta for %s", d.Key)
		}
		p := path.Join(r.SourceDir, file)
		text, err := e.Host.ReadFile(ctx, p)
		if err != nil {
			return nil, err
		}
		desc, err := vmdk.Parse(text)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(strings.ToLower(desc.CreateType), "sparse") || !strings.EqualFold(desc.ParentCID, d.Descriptor.CID) ||
			desc.ParentHint != path.Base(d.Source) || len(desc.Extents) != 1 {
			return nil, fmt.Errorf("the snapshot delta for %s does not attach to the source disk as expected", d.Key)
		}
		extent := desc.Extents[0].File
		if extent != path.Base(extent) || esxi.ValidPath(extent) != nil {
			return nil, fmt.Errorf("the snapshot delta for %s has an unexpected extent", d.Key)
		}
		out[d.Key] = delta{p, path.Join(r.SourceDir, extent)}
	}
	return out, nil
}

// liveGuard runs between steps while the job's snapshot exists: the source is
// still the registered VM, and it still runs on exactly the same deltas.
func (e *Engine) liveGuard(ctx context.Context, r Report, name string, deltas map[string]delta) error {
	src, dst, err := e.registrations(ctx, r.SourceVMX, r.TargetVMX)
	if err != nil {
		return err
	}
	if src != r.VM.ID || dst != 0 {
		return fmt.Errorf("VM registration changed during the live migration")
	}
	now, err := e.liveDeltas(ctx, r, name)
	if err != nil {
		return err
	}
	for k, d := range deltas {
		if now[k] != d {
			return fmt.Errorf("the VM's disks changed during the live migration")
		}
	}
	return nil
}

func (e *Engine) ownSnapshotOnly(ctx context.Context, id int, name string) error {
	tree, err := e.Host.Snapshot(ctx, id)
	if err != nil {
		return err
	}
	names, err := esxi.SnapshotNames(tree)
	if err != nil {
		return err
	}
	if len(names) != 1 || names[0] != name {
		return fmt.Errorf("the snapshot tree is not exactly this job's snapshot")
	}
	return nil
}

// liveMetadata returns the snapshot metadata that goes with the deltas: the
// VMSD, which must describe only the job's snapshot, and its VMSN.
func (e *Engine) liveMetadata(ctx context.Context, r Report, name string) ([]string, error) {
	files, err := e.Host.List(ctx, r.SourceDir)
	if err != nil {
		return nil, err
	}
	want := strings.TrimSuffix(path.Base(r.SourceVMX), path.Ext(r.SourceVMX)) + ".vmsd"
	vmsd := ""
	for _, f := range files {
		if strings.EqualFold(path.Ext(f), ".vmsd") {
			if vmsd != "" || path.Base(f) != want {
				return nil, fmt.Errorf("unexpected snapshot metadata file %s", path.Base(f))
			}
			vmsd = f
		}
	}
	if vmsd == "" {
		return nil, fmt.Errorf("the snapshot metadata file is missing")
	}
	text, err := e.Host.ReadFile(ctx, vmsd)
	if err != nil {
		return nil, err
	}
	c, err := vmx.Parse(text)
	if err != nil {
		return nil, err
	}
	vmsn := c["snapshot0.filename"]
	if c["snapshot.numsnapshots"] != "1" || c["snapshot0.displayname"] != name || c["snapshot0.numdisks"] != strconv.Itoa(len(r.Disks)) ||
		vmsn != path.Base(vmsn) || !strings.EqualFold(path.Ext(vmsn), ".vmsn") || esxi.ValidPath(vmsn) != nil {
		return nil, fmt.Errorf("the snapshot metadata does not describe exactly this job's snapshot")
	}
	bases := map[string]bool{}
	for _, d := range r.Disks {
		bases[path.Base(d.Source)] = true
	}
	for i := range r.Disks {
		if !bases[c[fmt.Sprintf("snapshot0.disk%d.filename", i)]] {
			return nil, fmt.Errorf("the snapshot metadata names an unexpected disk")
		}
	}
	return []string{vmsd, path.Join(r.SourceDir, vmsn)}, nil
}

// sameCID: a delta names its parent by CID, and the clone must keep it for the
// copied delta to attach. ESXi 6.5 keeps it; anything else stops here.
func sameCID(ctx context.Context, h Host, d Disk) error {
	raw, err := h.ReadFile(ctx, d.Target)
	if err != nil {
		return err
	}
	desc, err := vmdk.Parse(raw)
	if err != nil {
		return err
	}
	if !strings.EqualFold(desc.CID, d.Descriptor.CID) {
		return fmt.Errorf("the cloned disk has a different CID, so the snapshot delta cannot attach to it")
	}
	return nil
}

func (e *Engine) verifyDelta(ctx context.Context, r Report, d delta) error {
	target := path.Join(r.TargetDir, path.Base(d.descriptor))
	a, err := e.Host.ReadFile(ctx, d.descriptor)
	if err != nil {
		return err
	}
	b, err := e.Host.ReadFile(ctx, target)
	if err != nil || a != b {
		return fmt.Errorf("the copied delta descriptor differs from the source")
	}
	if err = e.Host.VerifyChain(ctx, target); err != nil {
		return fmt.Errorf("the target disk chain is not consistent: %w", err)
	}
	return e.sameSize(ctx, d.extent, path.Join(r.TargetDir, path.Base(d.extent)))
}

func (e *Engine) verifyCopies(ctx context.Context, r Report, files []string) error {
	for _, f := range files {
		if err := e.sameSize(ctx, f, path.Join(r.TargetDir, path.Base(f))); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) sameSize(ctx context.Context, a, b string) error {
	x, err := e.Host.Size(ctx, a)
	if err != nil {
		return err
	}
	y, err := e.Host.Size(ctx, b)
	if err != nil || x != y {
		return fmt.Errorf("the copy of %s differs in size from the source", path.Base(a))
	}
	return nil
}

// merged confirms a snapshot merge: no snapshot is left and the VMX names the
// base disks again.
func (e *Engine) merged(ctx context.Context, id int, vmxPath string, names map[string]string) error {
	tree, err := e.Host.Snapshot(ctx, id)
	if err != nil {
		return err
	}
	left, err := esxi.SnapshotNames(tree)
	if err != nil || len(left) != 0 {
		return fmt.Errorf("a snapshot is still present after merging")
	}
	raw, err := e.Host.ReadFile(ctx, vmxPath)
	if err != nil {
		return err
	}
	cfg, err := vmx.Parse(raw)
	if err != nil {
		return err
	}
	for k, want := range names {
		if cfg[k] != want {
			return fmt.Errorf("after merging, %s does not name the base disk", k)
		}
	}
	return nil
}

func sourceNames(r Report) map[string]string {
	names := map[string]string{}
	for _, d := range r.Disks {
		names[d.Key] = r.Config[d.Key]
	}
	return names
}

// restoreSource undoes a live migration that did not finish: the target is
// unregistered if it got registered, the source registered again, the job's
// snapshot merged and the source started. It refuses anything it cannot prove
// safe, such as a running target or a snapshot it did not take.
func (e *Engine) restoreSource(ctx context.Context, j *Job, r Report, name string) error {
	src, dst, err := e.registrations(ctx, r.SourceVMX, r.TargetVMX)
	if err != nil {
		return err
	}
	publishRegistrations(j, src, dst)
	if dst != 0 {
		if err = e.requireOff(ctx, dst); err != nil {
			return fmt.Errorf("the target is registered and not powered off")
		}
		unregisterErr := e.Host.Unregister(ctx, dst)
		if src, dst, err = e.registrations(ctx, r.SourceVMX, r.TargetVMX); err != nil || dst != 0 {
			return fmt.Errorf("the target could not be unregistered (%v)", unregisterErr)
		}
		publishRegistrations(j, src, dst)
	}
	if src == 0 {
		_, registerErr := e.Host.Register(ctx, r.SourceVMX)
		if src, dst, err = e.registrations(ctx, r.SourceVMX, r.TargetVMX); err != nil || src == 0 || dst != 0 {
			return fmt.Errorf("the source could not be registered again (%v)", registerErr)
		}
		publishRegistrations(j, src, dst)
	}
	tree, err := e.Host.Snapshot(ctx, src)
	if err != nil {
		return err
	}
	names, err := esxi.SnapshotNames(tree)
	if err != nil {
		return err
	}
	switch {
	case len(names) == 0:
		// The snapshot was never taken, or is already merged.
	case len(names) == 1 && names[0] == name:
		if err = e.Host.ConsolidateOwnSnapshot(ctx, src, name); err != nil {
			return err
		}
	default:
		return fmt.Errorf("the source has snapshots this job did not take")
	}
	if err = e.merged(ctx, src, r.SourceVMX, sourceNames(r)); err != nil {
		return err
	}
	if err = e.startSource(ctx, src); err != nil {
		return err
	}
	j.update(func(s *State) { s.SourcePower = "Powered on" })
	return e.Host.Finish(ctx, r.ID)
}

func (e *Engine) startSource(ctx context.Context, id int) error {
	p, err := e.Host.Power(ctx, id)
	if err != nil {
		return err
	}
	if p == esxi.On {
		return nil
	}
	powerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	powerErr := e.Host.PowerOn(powerCtx, id)
	cancel()
	deadline := time.Now().Add(e.Options.PowerOnTimeout)
	for time.Now().Before(deadline) {
		p, err := e.Host.Power(ctx, id)
		if err != nil {
			return err
		}
		message, err := e.Host.Message(ctx, id)
		if err != nil {
			return err
		}
		if t := strings.TrimSpace(message); t != "" && t != "No message." && t != "No message" {
			return fmt.Errorf("the source asks a question at power-on; answer it in Host Client")
		}
		if p == esxi.On {
			return nil
		}
		if err = pause(ctx, e.Options.PollInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("the source did not power on again (%v)", powerErr)
}
