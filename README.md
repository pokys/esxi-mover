# ESXi Mover

Moves or copies a powered-off VM between VMFS datastores on a **standalone ESXi**
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
2. **Choose.** Pick the VM, the target datastore and **Copy** or **Move**. The
   target folder keeps the source folder's name; if it is taken, a free name is
   suggested.
3. **Review.** The preflight shows where the VM goes and one verdict. Anything
   that blocks the migration is listed with its reason.
4. **Migrate.** The tool shuts the VM down gracefully, clones each disk thin,
   verifies the result and only then switches registration.

| Mode | Source afterwards | Target afterwards |
| --- | --- | --- |
| Copy | Registered, off | Verified, not registered |
| Move | Not registered, files kept | Registered, off, or on if chosen |

A moved VM keeps its identity (`uuid.action = "keep"`), so ESXi does not ask
"moved or copied?" at power-on. A copy is left without it: started next to its
original, it must get a new identity.

### Live migration (experimental)

**Keep the VM running while copying** migrates a running VM with a short
outage instead of a full cold copy:

1. The tool takes a snapshot of its own (no memory, no quiescing), so the base
   disks become read-only, and clones them to the target while the VM runs.
2. **Copy** then merges the snapshot back into the source. The copy holds the
   disks as they were at the snapshot, like a VM after a power cut.
3. **Move** shuts the VM down, copies only the snapshot deltas and metadata
   (what the guest wrote during the clone), verifies the whole chain, switches
   registration and starts the VM on the target, where the snapshot is merged
   while it runs. The outage is the guest shutdown plus the delta copy.

It is all or nothing. Until the VM runs on the target, any failure unregisters
the target if needed, merges the snapshot back into the source and starts the
source again. The tool only ever merges its own snapshot, named
`esxi-mover-…`: if the VM has any other snapshot, it stops for manual review.
The source datastore must have room for the changes written during the copy.

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

- **Closing the browser** does not stop anything. Reopen the page in the same
  browser and the job is shown again.
- **Clone or verification fails:** the source stays registered and off; the
  target folder may be partial. Inspect it, but don't use it.
- **SSH drops:** the clone keeps running on ESXi and is never started twice.
  After five minutes without SSH the page reports an unknown outcome.
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
