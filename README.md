# ESXi Mover

Moves or copies a VM between VMFS datastores on a **standalone ESXi**
host (Free editions included). No vCenter, no paid API: a small web app drives the
host over SSH and clones disks with `vmkfstools`.

**Source files are never deleted.** A MOVE only switches which copy is registered.

Tested against ESXi 6.5. Versions 6.7, 7.x and 8.x are covered by synthetic
fixtures only; see [TESTING.md](TESTING.md).

## Start

On any Linux host with Docker and the Compose plugin:

```sh
git clone https://github.com/pokys/esxi-mover.git
cd esxi-mover
sh ./start.sh
```

On Alpine Live (for example from netboot.xyz), install the tools first:

```sh
apk add git docker docker-cli-compose
```

`start.sh` starts Docker if needed, passes the machine's BIOS UUID (so the tool
refuses to migrate the VM it is running in) and runs the published image in the
foreground.

**Without a checkout**, for example in Dockge or Portainer, paste
[compose.yaml](compose.yaml) as a stack and start it.

Then:

1. Read the **admin token** and the **certificate fingerprint** from the log
   (`docker compose logs mover`, or the stack's log pane).
2. Open `https://APPLIANCE_IP:8443`, check the fingerprint and sign in.
3. When finished, run `docker compose down`.

To choose the token yourself, set `MOVER_ADMIN_TOKEN` in the stack's `.env`
(or `export` it before `start.sh`). The certificate is new on every start.

| Variable | Purpose |
| --- | --- |
| `MOVER_ADMIN_TOKEN` | Fixed admin token. Empty: random, printed to the log. |
| `MOVER_APPLIANCE_UUID` | BIOS UUID of this machine; `start.sh` fills it in. |
| `MOVER_IMAGE` | Another image tag or a locally loaded image. |

Use a trusted management network. The page accepts ESXi root credentials.

## Use

1. **Connect.** Enter the ESXi host, compare the SSH host key with the ESXi
   console, and sign in with a password or private key.
2. **Choose.** Pick the VM, the target datastore and **Copy only** or **Copy
   and switch**. The
   target folder keeps the source folder's name; if it is taken, a free name is
   suggested.
3. **Review.** The preflight shows where the VM goes and one verdict. Anything
   that blocks the migration is listed with its reason.
4. **Migrate.** The tool shuts the VM down gracefully, clones each disk thin,
   verifies the result and only then switches registration.

Both shut the VM down and copy it; they differ only in the last step.

| Operation | Source afterwards | Target afterwards |
| --- | --- | --- |
| Copy only | Registered, off | Verified, not registered |
| Copy and switch | Not registered, files kept | Registered, off, or on with **Start the target** |

A switched VM keeps its identity (`uuid.action = "keep"`), so ESXi does not ask
"moved or copied?" at power-on. A copy only is left without it: started next
to its original, it must get a new identity.

### Live migration (experimental)

**Shut down only at the end** changes only *when* the VM is shut down, never
the result: both operations end exactly as they do without it, and the tool
never starts the source.

1. The tool takes a snapshot of its own (no memory, no quiescing), so the base
   disks become read-only, and clones them to the target while the VM runs.
2. It then shuts the VM down and copies only the snapshot deltas and metadata,
   what the guest wrote during the clone, and verifies the whole chain.
3. **Copy only** registers the copy just long enough to merge its snapshot,
   then merges the source's snapshot too. **Copy and switch** switches
   registration, starts the target if chosen, and merges the snapshot there.

The outage is the time from the shutdown to the end instead of the whole copy.
Until registration switches, a failure restores the source registration and
merges the snapshot once any clone is confirmed to have ended. An unknown clone
outcome leaves the snapshot and operation lock for manual review. If the source
had already been shut down, it stays off. The tool only ever merges its own
snapshot, named `esxi-mover-…`:
if the VM has any other snapshot, it stops for manual review. The source
datastore must have room for the changes written during the copy.

Live migration requires at least **1 GiB free on the source**. While cloning,
the tool checks it on each poll. Below that reserve, or after five minutes
without a successful space check, it requests a stop and waits for the clone's
exit before merging the snapshot. This is a guard, not reserved space: fast
guest writes can still fill the datastore between checks or during a merge.

The **Technical log** at the bottom lists every SSH command with its exit code
and duration; **Copy** puts it on the clipboard.

## What it refuses

The tool stops rather than guess. It blocks:

- any snapshot, including stale snapshot files or metadata;
- suspended VMs, linked clones, RDM, shared or multi-writer disks,
  independent disks, encryption, vTPM, PCI passthrough;
- disks or NVRAM outside the VM folder;
- non-VMFS or VMFS-L datastores (only VMFS-5 and VMFS-6);
- a target without room for the allocated data + 15% + 1 GiB;
- the VM the appliance itself runs in.

It never repairs disks or cleans up files, and never merges a snapshot it did
not take itself.

Verification is structural (disk chain, thin format, capacity, extent size and
the exact VMX). It is not a checksum of every sector, and not a boot test.

## When something goes wrong

**Stop migration…** under the progress bar ends a migration before its first
irreversible step (registration, the live cutover, merging a snapshot); after
that it disappears. It ends the running clone and registers nothing. A cold
migration leaves the source as it is, off if it was already shut down. A live
one merges its snapshot back after the clone's exit is confirmed; the source
keeps running, as it has not been shut down yet. If exit cannot be confirmed,
the snapshot and lock remain for manual review.

- **Closing the browser** does not stop anything. Reopen the page in the same
  browser and the job is shown again.
- **Clone or verification fails:** the source stays registered and off; the
  target folder may be partial. Inspect it, but don't use it.
- **SSH drops:** the clone keeps running on ESXi and is never started twice.
  After five minutes without SSH the page reports an unknown outcome.
  For a live migration, confirm that the clone has ended before merging the
  temporary snapshot or archiving the operation lock.
- **The appliance restarts:** the disk being cloned finishes on ESXi. The
  remaining steps do not resume.

An unfinished operation leaves a lock in `/tmp/esxi-mover/active` on ESXi, and
the tool refuses new work while it exists. Once you have checked that no
`vmkfstools` is running and which VMX is registered, archive it on ESXi:

```sh
mv /tmp/esxi-mover/active /tmp/esxi-mover/reviewed-$(date +%s)
```

## Build

```sh
go test ./...
docker build -t esxi-mover:local .
MOVER_IMAGE=esxi-mover:local sh ./start.sh
```

Each push to `main` publishes `ghcr.io/pokys/esxi-mover:latest` after the tests
pass; a `v*` tag publishes that version.

MIT licensed.
