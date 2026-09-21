# ESXi Mover
<img width="784" height="723" alt="image" src="https://github.com/user-attachments/assets/9c987289-47b1-4d62-8352-75d10bcc7dfe" />

Copy or move a VM to another datastore on a **standalone ESXi host**, Free
edition included, from your browser. No vCenter and no paid API: a small web app
drives the host over SSH and clones the disks with `vmkfstools`.

**Source files are never deleted.** Switching to the copy only changes which
VM is registered.

## What it does

- **Copy only** or **Copy and switch**: both shut the VM down and copy it; the
  second then registers the copy in place of the source and can start it.
- **Shut down only at the end** *(experimental)*: the VM keeps running while
  its disks are copied and is off only for the final changes.
- Checks everything before touching anything, and stops rather than guesses.
- **Stop** at any time before the point of no return.
- Shows every SSH command it runs, with exit code and duration.

## Quick start

On a Linux machine with Docker (Alpine Live works too:
`apk add git docker docker-cli-compose`):

```sh
git clone https://github.com/pokys/esxi-mover.git
cd esxi-mover
sh ./start.sh
```

Or paste [compose.yaml](compose.yaml) as a stack into Dockge or Portainer.

1. Read the **admin token** and **certificate fingerprint** from the log.
2. Open `https://APPLIANCE_IP:8443`, check the fingerprint and sign in.
3. Run `docker compose down` when you are done.

| Variable | Purpose |
| --- | --- |
| `MOVER_IMAGE` | Image to run, e.g. the pinned `ghcr.io/pokys/esxi-mover:v1.0.0`. Default: `latest`. |
| `MOVER_ADMIN_TOKEN` | Your own admin token. Empty: a random one, printed to the log. |
| `MOVER_APPLIANCE_UUID` | This machine's BIOS UUID, so the tool refuses to move itself; `start.sh` fills it in. |

Use a trusted management network: the page takes ESXi root credentials.

## How it works

1. **Connect** to the host and compare its SSH key with the ESXi console.
2. **Choose** the VM, the target datastore and the operation. The target folder
   keeps the source folder's name, or a free one is suggested.
3. **Review** the preflight: where the VM goes, and one verdict.
4. **Migrate**: graceful shutdown, thin clone of every disk, verification, and
   only then the registration switch.

| Operation | Source afterwards | Target afterwards |
| --- | --- | --- |
| Copy only | Registered, off | Verified copy, not registered |
| Copy and switch | Not registered, files kept | Registered; running with **Start the target** |

The tool never starts the source. A switched VM keeps its identity, so ESXi
does not ask "moved or copied?"; a copy is left to get a new one.

**Shut down only at the end** takes a snapshot of its own, clones the now
read-only base disks while the VM runs, then shuts it down and copies only what
changed. The result is the same as without it. The source datastore needs at
least 1 GiB free for those changes. Only the tool's own `esxi-mover-…` snapshot
is ever merged; any other snapshot stops the migration for review.

## What it refuses

- VMs with any snapshot, including leftover snapshot files
- suspended VMs, linked clones, RDM, shared, independent or encrypted disks,
  vTPM, PCI passthrough
- disks outside the VM folder
- anything but VMFS-5 and VMFS-6
- a target without room for the used data + 15% + 1 GiB

Verification is structural (disk chain, format, size, exact VMX), not a
sector-by-sector checksum or a boot test.

## When something goes wrong

- **Stop migration…** under the progress bar ends the running clone and
  registers nothing. With *Shut down only at the end*, the source simply keeps
  running.
- **Closing the browser** stops nothing; reopen the page in the same browser
  to see the job.
- **Outcome unknown** means the connection was lost and the clone may still be
  running on ESXi. Check before touching any files.
- An unfinished job leaves a lock on ESXi. Once no `vmkfstools` runs, release it:

  ```sh
  mv /tmp/esxi-mover/active /tmp/esxi-mover/reviewed-$(date +%s)
  ```

## Build

```sh
go test ./...
docker build -t esxi-mover:local .
MOVER_IMAGE=esxi-mover:local sh ./start.sh
```

MIT licensed.
