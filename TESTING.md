# Testing

## What CI runs

Every push runs, on a GitHub Linux runner:

```sh
go vet ./...
go test -race -count=1 ./...
node --test scripts/test-web.mjs # browser state with simulated DOM and fetch
python3 scripts/test-start.py   # start.sh against fake docker and rc-service
docker compose config --quiet
docker build .                  # also runs the test suite inside the build
```

The image is published only after all of these pass.

The tests use synthetic ESXi 6.5, 6.7, 7.0 and 8.0 output in `fixtures/`, a
fake host for the migration engine, a real local SSH server for the transport
and a real POSIX shell for quoting and the detached clone worker. They cover:

- **Parsing:** inventory, datastores, VMX, VMXF, VMDK descriptors, power states
  and the "moved or copied" question.
- **Refusals:** every snapshot signal, shared disks and chains, unsupported
  devices and datastores, capacity, the appliance's own VM, and snapshots or
  changes appearing after Analyze.
- **Failure handling:** each failure before the registration switch leaves the
  source registered, lost replies are reconciled and never retried, a clone is
  never launched twice, rollback, power-on problems. Unknown live clone outcomes
  retain the snapshot and lock; low source space stops the clone before recovery,
  and an unconfirmed stop cannot merge the snapshot. COPY drops an inherited
  `uuid.action = "keep"`, including during live migration.
- **SSH:** host key pinned before any credentials are sent, keyboard-interactive
  only hosts, one shared connection, timeouts during commands and channel opening
  (including a retried connection), and
  credentials never appearing in errors or the audit log.
- **Web:** token, cookies, CSRF, Origin checks, request limits, login throttling.
  Expired idle sessions and failed inventory reads close their SSH connections.
  Browser tests cover rejected/lost start replies, locked analysis inputs,
  page restoration and the distinct unknown-outcome display. Alternating clone
  and space polls retain the earlier audit events.

## Verified on a real host

On a production ESXi 6.5 host:

- password sign-in over keyboard-interactive SSH, host key pinning;
- inventory including multi-line annotations, datastores, capability probe;
- preflight on VMs with CBT disks, VMware Tools VMXF and deleted snapshots;
- COPY, and MOVE with power-on (graceful shutdown, thin clone, verification,
  registration switch), with no "moved or copied" question at power-on;
- the target folder named after the source, and a free name offered on collision.

Before live migration was written, each of its steps was run by hand on the
same host with a running test VM: a snapshot without memory, `vmkfstools -i` of
the base disk while the VM ran (as fast as a cold clone), the clone keeping the
base disk's CID so the copied delta attaches unchanged, the delta and VMSD/VMSN
copied after a graceful shutdown (well under a second), registration of the
copy with its snapshot on another datastore, power-on without a question, and
merging the snapshot on the running copy and on the running source.
The tool's live MOVE then ran on that host end to end: clone while running,
about 38 s of outage (guest shutdown, delta copy and the full safety checks),
no question at power-on, and the snapshot merged on the running target.
It later ran the same way on a second host for a production Windows server:
about a minute of outage instead of about 17.

**Not yet exercised on a real host:** live COPY, the Stop link, SSH key sign-in,
Force Power Off, rollback, rejected registration, SSH loss or an appliance
restart during a clone, automatic stopping on low source space, and ESXi 6.7,
7.x and 8.x.

## Testing a new host or version

Use a disposable VM and a backup.

1. COPY first. Check the target disks with `vmkfstools -e` and that the source
   is untouched.
2. MOVE without power-on, then with it. The target should boot with the same
   BIOS UUID and MAC and no question from ESXi.
3. When a preflight blocks something it shouldn't, keep the audit log. It holds
   the exact ESXi output. Remove names, addresses and UUIDs before sharing it or
   turning it into a fixture.
