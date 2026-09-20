# ESXi Mover

A conservative, open-source **cold datastore copy/migration tool for standalone
VMware ESXi**, including Free editions. Go backend, embedded WebGUI, SSH management,
no vCenter, no paid VMware API, no external database, no persistent application state.

**ESXi Mover NEVER deletes source VM files.** There is no source deletion API,
cleanup command, or deletion button. In MOVE, only the inventory registration changes.

This is an MVP intended for lab validation first. ESXi 6.5/6.7 are the primary targets;
the capability/parser layer also recognizes 7.x and 8.x. These versions have synthetic
fixture coverage, **not real ESXi certification**. See [TESTING.md](TESTING.md).

## Run

On Linux with Docker and the Compose plugin:

```sh
sh ./start.sh
```

Open **https://APPLIANCE_IP:8443**. Compare the certificate SHA256 fingerprint
with the console, then enter the startup admin token. The self-signed certificate
and key are generated in RAM. Verify the SSH fingerprint separately through a
trusted channel before submitting ESXi credentials. The initial SSH probe aborts
before authentication; every authenticated connection pins the confirmed key.

Keep the console attached: Docker persistent logging is disabled. The token is
valid for this application run. It is not saved by the app or exposed in responses.
Use a trusted management network; do not expose the root-credential UI publicly.

## Alpine Live quick start

Boot Alpine Live (for example via netboot.xyz), obtain DHCP and run as root.
The repository is [pokys/esxi-mover](https://github.com/pokys/esxi-mover).
Authenticate to GitHub/GHCR first if the repository or container package is private.

```sh
apk update
apk add git docker docker-cli-compose
git clone https://github.com/pokys/esxi-mover.git
cd esxi-mover
MOVER_IMAGE=ghcr.io/pokys/esxi-mover:latest sh ./start.sh
```

The script starts Docker via OpenRC when needed, pulls the selected image, reads the
appliance BIOS UUID from DMI when available, then runs the WebGUI in the foreground.
The container runs as a non-root user with a read-only filesystem, no added
capabilities, no volumes and a 256 MiB memory limit. State, credentials and audit
events remain in RAM. A reboot loses the application's state by design.

### Alpine Live: Docker blocked by the networking service

If OpenRC reports `ifquery: could not parse /etc/network/interfaces` and
`cannot start docker as networking would not start`, Docker is blocked by a
host network configuration error. This happens before ESXi Mover starts.
Do not overwrite the interfaces file or restart a working network just to launch
the application.

For a temporary Live session where networking already works (for example, the
repository was just cloned successfully), prepare Docker's filesystem dependencies
and start only Docker without dependency traversal:

```sh
rc-service sysfs start &&
rc-service cgroups start &&
rc-service --nodeps docker start
docker info
```

Proceed only when `docker info` succeeds. From the project directory, pull the
prebuilt image to avoid a Go/Docker build in the Live system's RAM filesystem:

```sh
MOVER_IMAGE=ghcr.io/pokys/esxi-mover:latest sh ./start.sh
```

This is a temporary workaround for an already-connected Live host. It does not
repair `/etc/network/interfaces`; fix that configuration separately before relying
on networking after a reboot. The normal script still starts Docker with its
dependencies and never applies this workaround automatically. If the package is
private, authenticate to GHCR as described below before pulling.

References: [Alpine's Docker service dependencies](https://github.com/alpinelinux/aports/blob/master/community/docker/docker.initd)
and [OpenRC's `--nodeps` option](https://github.com/OpenRC/openrc/blob/master/man/rc-service.8).

## GitHub-built Docker images

The GitHub Actions workflow builds a `linux/amd64` image on pushes and pull requests.
It runs Go checks and race tests, builds the Dockerfile and checks the container
entrypoint. Only after these pass does a separate publishing job upload to
**`ghcr.io/pokys/esxi-mover`**:

- Push to the repository's default branch: `latest` and `sha-FULL_COMMIT_HASH`.
- Push a `v*` tag, for example `v1.0.0`: that exact tag and `sha-FULL_COMMIT_HASH`.
- Manual **Run workflow** on the default branch: publish its current image.
- Pull requests and other branches: validate the build without publishing.

`latest` follows the default branch; a version tag does not replace it. GitHub's
provided `GITHUB_TOKEN` authenticates publication; no personal access token or
Docker Hub secret is needed. Only the publishing job has `packages: write`.
Actions are pinned to full commit hashes; images include SBOM and build provenance.
Follow builds in [GitHub Actions](https://github.com/pokys/esxi-mover/actions).
Actions must be enabled and repository policies must permit package writes.

For anonymous pulls, set the GHCR package visibility to **Public** after its first
publication. A public repository alone does not make a newly published package
public. Otherwise authenticate to GHCR before pulling the private package.

For a private package, run `docker login ghcr.io -u pokys` and enter a GitHub
personal access token (classic) with `read:packages` at the password prompt. Do
not use the GitHub account password or put the token in a command, repository or
support screenshot. See [GitHub's registry authentication guide](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry#authenticating-with-a-personal-access-token-classic).

Once an image exists, use the checkout's normal start script on Alpine:

```sh
MOVER_IMAGE=ghcr.io/pokys/esxi-mover:latest sh ./start.sh
# Or select a tested version:
MOVER_IMAGE=ghcr.io/pokys/esxi-mover:v1.0.0 sh ./start.sh
```

This pulls the selected image and starts it without compiling Go on the appliance.
For an already-loaded/cached image without a registry connection, add
`MOVER_SKIP_BUILD=1`. With no `MOVER_IMAGE`, the default remains a local source build.

The workflow follows GitHub's [container publishing documentation](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images).

## Small appliance memory and offline images

**RAM sizing:** 512 MiB–1 GiB is the intended runtime range, not a measured promise.
Building Go and Docker layers in an Alpine RAM filesystem can need substantially
more memory. For a small diskless VM, build the image elsewhere and load it:

```sh
docker load -i esxi-mover-image.tar
MOVER_SKIP_BUILD=1 sh ./start.sh
```

The source build also works on a host with more RAM. Optionally restrict exposure:
`MOVER_BIND_ADDRESS=192.0.2.20 sh ./start.sh` (use the appliance's management IP).

If using the delivered source archive and image instead of a Git remote, transfer
`esxi-mover-source.tar.gz` and `esxi-mover-image.tar` to the Alpine VM, then run from
the directory containing both files:

```sh
apk update
apk add docker docker-cli-compose
tar -xzf esxi-mover-source.tar.gz
rc-service docker start
docker load -i esxi-mover-image.tar
cd esxi-mover
MOVER_SKIP_BUILD=1 sh ./start.sh
```

## COPY and MOVE

| Mode | Source after success | Target after success |
| --- | --- | --- |
| COPY | Registered, off, all files retained | Complete, verified, unregistered |
| MOVE | Unregistered, all files retained | Registered, off by default |
| MOVE + Power On | Unregistered, all files retained | Registered, on after confirmed success |

1. Connect, select VM and a different target VMFS datastore.
2. Choose COPY or MOVE. Power On is a separate, default-off MOVE option.
3. Analyze the report, disk sizes, warnings and blocking checks.
4. Confirm a current backup and exclusive maintenance access; start the job.
5. Repeat the full preflight and acquire an atomic ESXi host operation lock.
6. Request graceful shutdown; poll for actual `Powered off`.
7. Recheck identity, snapshots and dependencies after shutdown and before each disk.
8. Clone disks sequentially with `vmkfstools -i SOURCE TARGET -d thin`.
9. Verify each target disk, copy selected configuration, verify VMX, then reverify all disks.
10. Only now, for MOVE, unregister source and register target. Optionally power target on.

The source is never automatically restarted. Force Power Off requires a separate
explicit confirmation and may lose guest data. Wait and manual-shutdown actions
always recheck the actual host state; clicking a button is never proof of shutdown.

The tool does not directly edit source VMX, VMDK, CID or parentCID. ESXi itself may
update VMX runtime fields, NVRAM and logs during a normal shutdown/registration;
"source files preserved" does not claim that VMware's own metadata is bit-identical.

## Safety checks and supported scope

Supported implementation scope: standalone/Free ESXi over SSH, VMFS to VMFS,
one VM at a time, multiple ordinary persistent VMFS disks, thin destination,
password or private-key authentication, graceful shutdown and optional target power-on.
Relative and absolute disk paths are accepted when they resolve to the same VM directory.
Thin and thick source descriptors are supported; only thin targets are written.

Every active snapshot **blocks migration**. Independent signals are combined:

- `vim-cmd vmsvc/snapshot.get` must have a recognized empty-tree response.
- Active VMX disk filenames must not match a numbered snapshot pattern.
- Every descriptor must be standalone, with `parentCID=ffffffff` and no parent hint.
- Delta, seSparse, numbered snapshot, VMSN or VMSS files require manual review.
- VMSD must be absent, empty, or unambiguously describe zero snapshots without stale entries.

**ESXi Mover never consolidates, removes, repairs or modifies snapshots.**
V1 blocks orphan-looking snapshot files too: it cannot prove they are safe orphans.
The same checks run during Analyze, at Start, after shutdown, before each clone and
before committing the MOVE. An external actor can still modify a VM between SSH
commands: **the SSH workflow is not an atomic transaction against other admins**.
Disable conflicting backup/snapshot jobs, auto-starts and orchestration, and reserve
the maintenance window and datastore capacity. The Mover lock coordinates other
Mover instances; it cannot lock out an ESXi administrator.

The capacity gate uses **total provisioned bytes + 15% + 1 GiB**, even when `du -k`
reports a smaller allocation. The report also shows allocated bytes, or clearly marks
them unknown. Remaining capacity is checked again between disk clones.
No space reservation against unrelated datastore writers is provided.

Target verification checks `vmkfstools -e`, descriptor readability, standalone chain,
thin format, expected virtual capacity, local extent existence/logical size and
exact parsed VMX contents. **This is structural verification, not a sector-by-sector
checksum or guest application/boot verification.** The VM's controller and UUID/MAC
configuration are retained in the rewritten VMX.

## Unsupported / deliberately blocked

- Live/warm migration, snapshot pre-copy, CBT, delta synchronization and linked clones.
- Active snapshots, suspicious stale/orphan snapshot metadata, suspended VMs.
- RDM, shared VMDK/extents, multi-writer, shared SCSI bus, nonpersistent/independent disks.
- vSAN, non-VMFS stores, VMFS-L/system stores, unknown VMFS versions, encrypted VMs/disks, vTPM and PCI passthrough. Only VMFS-5 and VMFS-6 are allowed.
- Disks, extents or NVRAM outside the VM directory, including nested/cross-datastore disks.
- Binary/unreadable/unrecognized descriptors, split extents, broken disk chains.
- Writable serial/parallel files, connected floppy devices and unknown active devices/backings.
- External/team VMXF dependencies; simple standalone VMXF with a relative unchanged VMX name works.
- Unclassified datastore paths, duplicate keys, unsupported encodings or control-character paths.
- A selected Mover appliance matching the configured BIOS/DMI UUID (including SMBIOS byte order).

Other registered VMs are inspected for shared disk/extent dependencies. An unreadable
other-VM configuration or a parent chain in another VM blocks the operation because
V1 cannot prove dependency isolation. Unregistered VMs and other hosts are not visible
to that inventory scan; independent administrative ownership of the disks is required.

External ISO media is not migrated. Relative ISO references are rewritten to their
existing source location and shown as a warning. No arbitrary directory copy occurs:
only VMX and explicitly referenced local NVRAM/validated VMXF are copied. Swap, locks,
logs, memory, CBT and snapshot files are not copied.

Unknown/localized critical output stops the operation. Legacy insecure SSH algorithms
are not automatically enabled; an ESXi host unable to negotiate the SSH library's
supported algorithms is unsupported until its SSH configuration is reviewed.
Self-migration detection is unavailable when the appliance UUID cannot be read/provided;
the tool does not guess from a VM name.

## Disconnects, transient metadata and recovery

Each clone runs under `nohup sh` **on ESXi**, with stdin/stdout/stderr detached. Runtime
metadata is under `/tmp/esxi-mover/active/`: `job.meta`, `disk-N.pid`, `disk-N.log`,
`disk-N.exit`, and an exclusive `disk-N.started` directory. Exit codes are published
through an atomic rename. The remote worker checks the power state immediately
before `vmkfstools`. GUI polling reads PID liveness, output/progress and exit status.
No throughput/ETA numbers are invented.

Short SSH failures are observed for up to five minutes without relaunching a disk.
After that the UI stops with an unknown outcome; the ESXi worker may still be running.
A browser disconnect does not cancel the backend. An Alpine restart loses the state
machine, but the current detached disk may finish; subsequent disks, verification and
registration do not automatically resume. ESXi reboot is outside this guarantee.

| Failure | State and next step |
| --- | --- |
| Clone/verification/config failure | Source remains registered and off. Target may be partial. Inspect it; do not register/boot it as a complete copy. |
| SSH/Alpine disconnect | Inspect `job.meta`, log, PID and exit marker on ESXi; determine whether a worker is still active. Do not start another job blindly. |
| Target registration rejected | Inventory is reconciled. If target is definitely absent, the tool immediately attempts source re-registration, leaving it off. |
| Registration reply lost | Check actual canonical VMX inventory paths. If inventory is unreadable/ambiguous, stop for manual reconciliation; never create duplicate registrations on an assumption. |
| Target Power On fails | Target stays registered; source files remain. Resolve the VM question/failure in Host Client. Explicit registration rollback is available only while target is confirmed off. |
| Rollback result unknown | Reconcile both VMX paths manually; do not boot either copy until ownership and registration are clear. |

For the moved/copied VM question, the tool parses the actual question and the unique
English `I moved it` choice (including VMware's `_moved` spelling). It never assumes
an answer index. Unknown, localized or ambiguous questions require manual action.

A failed/uncertain operation retains the active lock. Connect/Analyze displays the
existing operation and blocks conflicting work. Successful operations move metadata
to `/tmp/esxi-mover/completed-JOB_ID/`; it is not an application history database.
After independently confirming **all** workers have exited and registration is safe,
an administrator can archive the exact active metadata directory manually:

```sh
# Run on ESXi only AFTER inspecting the operation, PID, exit codes and both VMX paths.
# Choose a unique archive name. This moves only the transient Mover metadata.
mv /tmp/esxi-mover/active /tmp/esxi-mover/reviewed-UNIQUE_ID
```

Never archive an active operation's metadata while its worker is running. There is
no automatic stale-lock override, no job-kill button, no resumable workflow after
appliance reboot, and no target/source cleanup operation. Files remain for deliberate
administrator inspection. Do not register/power both identity-preserving copies on
the same network. For an independently booted COPY, use Host Client to assign a new
identity and isolate its NICs as appropriate before testing.

## Development

Go 1.26+ is required by the current dependency set; development used Go 1.27.1.

```sh
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/esxi-mover -listen 127.0.0.1:8443
```

The only direct Go dependency is `golang.org/x/crypto/ssh`. Frontend assets have no
external CDN or npm dependency. All state-changing requests require a RAM session,
same-origin JSON and CSRF token; cookies are Secure, HttpOnly and SameSite Strict.
Logs are bounded to 500 in-memory audit events and 8 KiB of latest clone output.
Credentials are not returned in HTML/JSON, command categories or error messages.
Disk/config bytes read through SSH are kept exact and never included in audit logs.

Cross-build and package a portable scratch image without a Docker daemon:

```sh
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w -buildid=' -o dist/esxi-mover ./cmd/esxi-mover
python3 scripts/package-image.py dist/esxi-mover dist/esxi-mover-image.tar
docker load -i dist/esxi-mover-image.tar
```

The archive contains a non-root scratch image with the same entrypoint and exposed
port as Dockerfile. Its `.sha256` file verifies archive transport. Building an archive
does not establish that it was run in Docker. `start.sh` defaults to a normal multi-stage
Docker build, which also runs the test suite in the builder image.

```text
cmd/esxi-mover/     HTTPS application entrypoint
internal/esxi/     SSH executor, inventory, quoting, detached workers, audit
internal/vmx/      VMX parser, device analysis, rewrite, VMXF validation
internal/vmdk/     descriptor parser and standalone-disk rules
internal/migration/ preflight, snapshot checks, state machine, verification, rollback
internal/web/      sessions, CSRF, endpoints, ephemeral TLS, embedded UI
fixtures/          synthetic ESXi 6.5/6.7/7.0/8.0 and disk/config samples
scripts/           portable container image packaging
.github/workflows/ test, race, vet, Docker build and GHCR publication
Dockerfile · compose.yaml · start.sh · TESTING.md · LICENSE
```

Implementation references: Broadcom's [vmkfstools cloning guidance](https://knowledge.broadcom.com/external/article/343140/cloning-and-converting-virtual-machine-d.html),
[disk-chain verification](https://knowledge.broadcom.com/external/article/309366),
and [VM question/choice format](https://knowledge.broadcom.com/external/article?legacyId=1026835).

MIT licensed. No source cleanup, snapshot repair or live migration is planned for V1.
