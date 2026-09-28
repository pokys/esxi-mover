package migration

import (
	"path"
	"strconv"
	"strings"

	"esxi-mover/internal/esxi"
	"esxi-mover/internal/vmx"
)

// advice turns a blocking check into a plain-language title, why it matters,
// and the steps that clear it. It never relaxes a check: every answer ends in a
// change the operator makes and a fresh analysis. Commands carry real, quoted
// paths, never placeholders, so they can be pasted into an ESXi shell as they are.
type advice struct {
	title, why string
	steps      []Step
}

func say(text string) Step          { return Step{Text: text} }
func command(text, cmd string) Step { return Step{Text: text, Command: cmd} }
func again() Step                   { return say("Analyze again.") }
func advise(title, why string, s ...Step) advice {
	return advice{title, why, s}
}

func blockAdvice(r *Report, name, detail string) advice {
	switch name {
	case "ESXi version":
		return advise("Unsupported ESXi version",
			"This tool reads the host's command output to decide what is safe. On a version it was not built for, a misread answer could lead to a wrong decision in the middle of a migration.",
			command("Check the version the host reports.", "vmware -vl"),
			say("Only ESXi 6.5, 6.7, 7.x and 8.x are supported. Move this VM with vCenter or an export and import instead."))
	case "Existing operation":
		return advise("Another migration holds the host lock",
			"Two migrations at once could work on the same VM or folder. A migration that failed keeps the lock on purpose, because it may have left the source unregistered or a half-written target that someone has to look at.",
			say("If a migration is running, wait for it to finish."),
			say("If one ended failed or uncertain, open it and resolve it first, and check its target folder."),
			command("Only when no migration is running, archive the lock. A host reboot also clears it.", "mv /tmp/esxi-mover/active /tmp/esxi-mover/reviewed-$(date +%s)"),
			again())
	case "Target datastore", "Source datastore":
		if strings.Contains(detail, "VMFS") {
			return advise("Unsupported datastore",
				"The copy relies on VMFS locking and vmkfstools. On other datastore types this tool cannot guarantee that the copy is complete and that nothing else writes to it.",
				say("Only mounted VMFS-5 and VMFS-6 datastores are supported, not vSAN, NFS or vVols. The type is shown in the host client under Storage."),
				say("Choose another target datastore, or move this VM with vCenter."))
		}
	case "Power state":
		return advise("The VM is suspended",
			"Its running state lives in files this tool does not copy. The copy would start from its disks alone and the suspended session would be lost.",
			say("Power the VM on. It resumes from the saved state."),
			say("Shut it down from the guest operating system."),
			again())
	case "Mover appliance":
		return advise("This is the ESXi Mover appliance",
			"A migration shuts the VM down. The appliance would switch itself off halfway through its own migration.",
			say("Move it with the host client, or run a second appliance on another host."))
	case "Snapshot Manager":
		return advise("The VM has snapshots",
			"With a snapshot, everything written since it was taken lives in separate delta files. Copying the base disks would silently lose that data.",
			command("See the snapshot tree.", "vim-cmd vmsvc/get.snapshotinfo "+strconv.Itoa(r.VM.ID)),
			say("If a backup is running, wait for it to finish; it deletes its own snapshot."),
			say("Otherwise open Snapshots in the host client and choose Delete all, which merges them into the disks. If the list is empty, choose Consolidate."),
			again())
	case "VMSD metadata":
		return advise("Stale snapshot metadata",
			"The snapshot list names snapshots that ESXi does not report. This tool cannot tell whether one still exists, and if it does, its data would be missing from the copy.",
			command("See what the .vmsd file in the VM folder still names.", "cat "+esxi.Quote(r.SourceDir)+"/*.vmsd"),
			say("Make sure Snapshots in the host client is empty, then choose Consolidate."),
			say("If entries remain, ESXi rewrites the file at the next snapshot operation, such as the next backup run."),
			again())
	case "Active snapshot chain":
		return advise("The VM runs on a snapshot",
			"Its current data is spread across a chain of files. Copying only part of the chain would silently lose everything written since the snapshot.",
			say("Open Snapshots in the host client and choose Delete all or Consolidate."),
			again())
	case "Disks from other folders":
		return advise("Disks from other folders need the VM off",
			"While the VM runs, ESXi writes a snapshot for every disk. For a disk outside the VM folder, that snapshot and its merge back have never been verified on a real host.",
			say("Turn off Shut down only at the end, so the VM is shut down before the copy starts."),
			again())
	case "Configuration files":
		return advise("Unexpected NVRAM or VMXF file",
			"NVRAM holds the firmware settings, including the boot order. With the wrong file the copy may not boot, or boot with another VM's settings.",
			command("Check the nvram and extendedConfigFile lines.", "grep -i -E '^(nvram|extendedconfigfile)' "+esxi.Quote(r.SourceVMX)),
			say("With the VM off, a missing NVRAM line can be removed; ESXi creates a fresh file at the next power-on."),
			again())
	case "VMXF metadata":
		return advise("The VMXF file belongs to another VM",
			"The copy would carry another VM's metadata and could be mistaken for it.",
			say("With the VM off, remove the extendedConfigFile line from the VMX. ESXi writes a new .vmxf when it needs one."),
			again())
	case "Shared VMDK", "Shared extent", "Shared disk inventory":
		return advise("A disk is shared",
			"Another device or VM uses the same disk. After a move one of them would point at a disk that is gone, or both would write to different copies and the data would split.",
			say("Find which VMs use it in the host client, detach the shared disk from one of them, or move them together with vCenter."),
			again())
	case "Target filename":
		return advise("Two disks would get the same name",
			"In the target folder one disk would take the other's place, and the VM would start with the wrong disk.",
			say("With the VM off, rename one of them with vmkfstools -E and update the VMX."),
			again())
	case "VMDK descriptor", "VMDK format", "Disk extent", "Source disk chain":
		return advise("Unsupported disk",
			"This tool only copies disk layouts it fully understands. Anything else could produce a copy that looks fine but is incomplete.",
			say("Only ordinary thick or thin VMFS disks with one data file are supported. With the VM off, check the disk with vmkfstools -e."),
			say("Convert an unusual disk with vmkfstools -i into a thin VMFS disk, then attach the copy."),
			again())
	case "Disks":
		return advise("No disk can be copied",
			"Without a disk there is no VM to move.",
			say("The other blocks name the disks that were rejected. Clear those first."))
	case "ISO reference":
		return advise("CD/DVD points to an unreadable ISO",
			"The copy would point at a path that cannot be resolved, and ESXi would refuse to power it on.",
			say("Disconnect the CD/DVD drive or set it to Host device in Edit settings."),
			again())
	case "External configuration reference":
		return advise("The VMX points outside the VM folder",
			"The line "+strings.TrimPrefix(detail, "Unclassified datastore path in ")+" names a file this tool cannot rewrite, so the copy could keep writing into the source folder.",
			say("With the VM off, remove that line or point it into the VM folder."),
			again())
	case "Free space":
		return advise("Not enough space on the target",
			"A copy that runs out of space fails halfway, and a full datastore also stops every VM running on it.",
			say("Free up space on the target datastore or choose a larger one."),
			again())
	case "Source free space":
		return advise("Not enough space on the source",
			"While the VM keeps running, its changes go into a snapshot on the source. If that datastore fills up, ESXi pauses the VM and every other VM on it.",
			say("Free up at least 1 GiB on the source datastore, or turn off shutting down at the end and migrate with the VM off."),
			again())
	case "VM configuration":
		return vmxAdvice(r, detail)
	}
	return advice{}
}

func vmxAdvice(r *Report, detail string) advice {
	switch {
	case strings.Contains(detail, "ctk"):
		return advise("Changed Block Tracking is on",
			"The tracking files describe changes on the old disks. On the copy they would no longer match, and the next incremental backup could silently miss data.",
			command("Backup software usually turns it on. See where it is set.", "grep -i ctk "+esxi.Quote(r.SourceVMX)),
			say("Shut the VM down from the guest."),
			say("In Edit settings › VM options › Advanced › Configuration parameters, set ctkEnabled and every scsiX:Y.ctkEnabled to FALSE."),
			again(),
			say("Your backup turns it back on at its next run."))
	case strings.Contains(detail, "checkpoint.vmstate"):
		return advise("The VM has saved suspend state",
			"Its running state lives in files this tool does not copy. The copy would start from its disks alone and the suspended session would be lost.",
			say("Power the VM on so it resumes from that state."),
			say("Shut it down from the guest operating system."),
			again())
	case strings.HasPrefix(detail, "VMX forces an identity change"):
		return advise("The VMX forces a new identity",
			"ESXi would give the copy a new UUID, and licensing, backup and monitoring would treat it as a different machine.",
			say("With the VM off, remove the uuid.action line or set it to keep. The tool sets the right value on the target itself."),
			again())
	case strings.HasPrefix(detail, "Encryption or vTPM"):
		return advise("Encrypted VM or vTPM",
			"The keys belong to a key provider this tool cannot reach. The copied files could not be opened, and the VM would not start.",
			say("Move this VM with vCenter."))
	case strings.HasPrefix(detail, "Shared or multi-writer"):
		return advise("Shared or multi-writer disk",
			"Other cluster members keep writing to the disk. A copy would catch it mid-change, and the cluster would split between two disks.",
			say("Move the cluster with vCenter, or remove the sharing if nothing uses it."))
	case strings.HasPrefix(detail, "Only ordinary persistent"):
		return advise("Independent or non-persistent disk",
			"Non-persistent disks drop their changes at power-off, and independent disks are left out of the snapshot a live migration relies on.",
			say("With the VM off, set the disk mode to Dependent in Edit settings."),
			again())
	case strings.HasPrefix(detail, "Redo disk"):
		return advise("Old redo log reference",
			"A redo log can hold changes that are not in the disk yet. Leaving it behind would lose them.",
			say("With the VM off, check that the redo file is unused and remove that line from the VMX."),
			again())
	case strings.HasPrefix(detail, "Raw or passthrough"), strings.HasPrefix(detail, "PCI passthrough"):
		return advise("RDM or passthrough device",
			"An RDM only maps a physical LUN, so copying it copies no data. Passthrough hardware exists only on this host.",
			say("Remove the device now and add it again on the target, or move the VM with vCenter."),
			again())
	case strings.HasPrefix(detail, "Unrecognized or inactive VMDK"):
		return advise("Leftover disk entry",
			"The VMX names a disk that is not attached. This tool cannot tell whether the VM still needs its data.",
			say("With the VM off, remove the leftover lines for that device."),
			again())
	case strings.HasPrefix(detail, "Writable serial/parallel"):
		return advise("Serial or parallel port writes to a file",
			"The copy would keep writing into a file in the source folder.",
			say("Remove the port in Edit settings or give it another backing."),
			again())
	case strings.HasPrefix(detail, "Connected floppy"):
		return advise("Floppy drive connected",
			"The floppy image would stay on the source, and the copy would still depend on it.",
			say("Remove or disconnect the floppy drive in Edit settings."),
			again())
	case strings.HasPrefix(detail, "Unrecognized active device"):
		return advise("Unknown device backing",
			"A connected device uses a file this tool does not know, so it cannot tell whether that file has to move with the VM.",
			say("Disconnect or remove it in Edit settings."),
			again())
	case strings.HasPrefix(detail, "No supported active VMDK"):
		return advise("No disk attached",
			"Without an ordinary VMDK disk there is nothing to move.",
			say("Attach the VM's disk, or move it another way."))
	}
	return advice{}
}

// toolsAdvice explains a running VM without VMware Tools: the migration still
// works, but the shutdown it needs will wait for the operator.
func toolsAdvice(live bool) advice {
	when, meanwhile := "before anything is copied", "Nothing is copied or changed until the VM is off."
	if live {
		when, meanwhile = "after the copy, at the end", "Until then the VM keeps running on the migration's own snapshot; Stop merges it back and leaves the VM running."
	}
	return advise("The VM cannot be shut down automatically",
		"A graceful shutdown goes through VMware Tools in the guest. Without it the migration pauses "+when+" and waits for you. "+meanwhile,
		say("Install or start VMware Tools in the guest and analyze again, or plan to shut the guest down yourself when the migration asks."),
		say("When it waits, shut the guest down from its own console and choose I shut the VM down manually. Force Power Off is like pulling the plug and can lose data the guest had not written yet."))
}

// diskLocationAdvice names the disk that is not directly in the VM folder and
// says where it is. There are two ways out: leave the disk where it is and
// detach it for the migration, or copy it into the VM folder first.
func diskLocationAdvice(r *Report, ref vmx.DiskRef, reason error, ds []esxi.Datastore, files []string) (string, advice) {
	device := strings.TrimSuffix(ref.Key, ".filename")
	detail := device + " uses " + ref.File
	full, e := esxi.ResolveReference(ref.File, r.SourceDir, ds)
	if e != nil || path.Dir(full) == r.SourceDir {
		return detail + ": " + reason.Error(), advise("A disk cannot be found",
			"The VMX names a disk this tool cannot open, so it cannot tell what the copy would be missing.",
			say("Open Edit settings and check the disk on "+device+". The file may have been deleted, or its datastore renamed or unmounted."),
			say("Remove the entry if the disk is no longer needed, or point it at the right file."),
			again())
	}
	title, where := "A disk lies in a subfolder", "a subfolder of the VM folder"
	if !strings.HasPrefix(full, path.Dir(r.SourceDir)+"/") {
		title, where = "A disk lies on another datastore", "another datastore"
		for _, d := range ds {
			if strings.HasPrefix(full, path.Join("/vmfs/volumes", d.UUID)+"/") {
				where = "datastore " + d.Name
			}
		}
	}
	// A copy must not overwrite a file already in the VM folder.
	taken := map[string]bool{}
	for _, f := range files {
		taken[path.Base(f)] = true
	}
	name := freeName(path.Base(full), taken)
	if r.Request.BringDisks {
		return detail + " on " + where + ": " + reason.Error(), advise(title,
			"Bring disks from other folders is on, but this disk cannot be brought along, so the copy would still point at the original.",
			say("Move the disk to a mounted VMFS-5 or VMFS-6 datastore, or detach it before the migration and attach it again afterwards."),
			again())
	}
	return detail + " on " + where, advise(title,
		"This tool copies the VM folder only. The disk would stay behind and the copy would still point at it, so two VMs could end up writing to the same disk.",
		say("Choose one of the ways below. All of them need the VM shut down."),
		say("Let this tool copy it: turn on Bring disks from other folders (experimental) and analyze again. The disk is copied into the target folder and the original stays where it is."),
		say("To keep the disk where it is: in Edit settings remove the disk on "+device+" without deleting it from the datastore, and migrate without starting the VM. Then add it back to the migrated VM as an existing hard disk on "+device+" and start it."),
		command("To bring the disk along: copy it into the VM folder. The source datastore needs room for it.", "vmkfstools -i "+esxi.Quote(full)+" "+esxi.Quote(path.Join(r.SourceDir, name))+" -d thin"),
		say("Then in Edit settings remove the old disk on "+device+" without deleting it, add "+name+" as an existing hard disk on "+device+", and check that the VM starts. Delete the old disk only after the migrated VM is verified."),
		again())
}

// artifactAdvice explains a snapshot or suspend file in dir, the VM folder or a
// folder holding one of its disks. The file still blocks: from the file alone
// its orphan status cannot be proven. The steps show how to prove it without
// changing anything, then what to do.
func artifactAdvice(r *Report, dir, name string, files []string) (string, advice) {
	full := path.Join(dir, name)
	quarantine := path.Join(path.Dir(dir), "_quarantine", path.Base(dir))
	// A reference can sit in the VM's own VMX and snapshot list, or in files
	// next to the artifact.
	look := esxi.Quote(r.SourceDir) + "/*.vmx " + esxi.Quote(r.SourceDir) + "/*.vmsd"
	if dir != r.SourceDir {
		look += " " + esxi.Quote(dir) + "/*.vmx " + esxi.Quote(dir) + "/*.vmsd"
	}
	lock := command("Confirm no process holds it. The output, or the end of /var/log/vmkernel.log, must show mode 0.", "vmkfstools -D "+esxi.Quote(full))
	move := func(names ...string) []Step {
		paths := []string{}
		for _, n := range names {
			paths = append(paths, path.Join(dir, n))
		}
		return []Step{
			command("In a maintenance window, move it out of its folder. On the same datastore a move is only a rename and can be undone.",
				"mkdir -p "+esxi.Quote(quarantine)+" && mv "+esxi.Argv(append(paths, quarantine+"/")...)),
			again(),
			say("Delete the quarantine folder only after the migrated VM is verified."),
		}
	}
	if strings.HasSuffix(strings.ToLower(name), ".vmss") {
		detail := name + " holds the saved state of a suspended VM"
		why := "If the VM still resumes from this file, the copy would lose its running state. If nothing uses it, it is harmless. The file alone does not show which, so this tool does not guess."
		if r.Power == esxi.Suspended || strings.TrimSpace(r.Config["checkpoint.vmstate"]) != "" {
			return detail, advise("The VM has saved suspend state", why,
				say("The VM is suspended or its VMX points at this state, so the file is in use."),
				say("Power the VM on so it resumes, then shut it down from the guest operating system."),
				again())
		}
		moved := []string{name}
		vmem := strings.TrimSuffix(name, path.Ext(name)) + ".vmem"
		for _, f := range files {
			if path.Base(f) == vmem {
				moved = append(moved, vmem)
			}
		}
		steps := []Step{
			say("Already checked: the VM is " + strings.ToLower(string(r.Power)) + " and its VMX points at no saved state. ESXi often leaves this file behind after a resume."),
			lock,
		}
		return detail, advise("Leftover suspend file", why, append(steps, move(moved...)...)...)
	}
	title, what := "Leftover snapshot disk", "a snapshot delta disk"
	if strings.HasSuffix(strings.ToLower(name), ".vmsn") {
		title, what = "Leftover snapshot state file", "a snapshot state file; backup software that fails to delete its snapshot often leaves one"
	}
	detail := name + " is " + what
	why := "If it still belongs to a snapshot ESXi lost track of, the data in it would be missing from the copy, without any error. The file alone does not show which, so this tool does not guess."
	steps := []Step{
		say("Already checked: the active VMX does not use it and Snapshot Manager lists no snapshot."),
		command("Confirm nothing references it. This must print nothing.", "grep -l "+esxi.Quote(name)+" "+look+" 2>/dev/null"),
		lock,
		say("If unsure, choose Consolidate under Snapshots in the host client instead."),
	}
	return detail, advise(title, why, append(steps, move(name)...)...)
}
