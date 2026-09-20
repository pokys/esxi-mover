# Validation and first ESXi lab run

## Validation performed

Development environment: Windows amd64; Go 1.27.1; portable Clang for Go's race
detector; Git's POSIX shell for worker/quoting execution; Docker Compose 5.5.1 for
configuration validation. No ESXi endpoint or local Docker daemon was available.
GitHub's Linux runner subsequently built and smoke-tested the container and
published it to GHCR in [this successful run](https://github.com/pokys/esxi-mover/actions/runs/35467439023).

| Check | Result |
| --- | --- |
| Unit/fixture/mock tests | 109 passing cases including subtests; 50 passing top-level tests, fuzz seed checks and examples |
| `go test -race -count=1 ./...` | Passed, no races reported |
| Opt-in visual fixture test | Skipped in normal suite; exercised separately in a browser |
| `go vet ./...` | Passed |
| `govulncheck` | No vulnerabilities in imported packages or reachable code; module-level notice GO-2026-5932 concerns the unused `openpgp` package |
| `node --check internal/web/static/app.js` | Passed; Node is a development check only |
| `sh -n start.sh` | Passed |
| SSH password authentication | Local SSH server fixtures cover a host advertising only `keyboard-interactive` and a host advertising only `password`; failure causes are named without leaking the password. **Not verified against a real ESXi host** |
| `docker-compose config --quiet` | Passed with the standalone Compose CLI |
| Windows app build and live HTTPS/authentication smoke test | Passed; peer certificate fingerprint matched startup output; Secure cookie and session API verified |
| Static Linux amd64 cross-build | Passed |
| Portable Docker image archive | Created; manifest/config/layer hashes and static ELF contents verified |
| Browser workflow with synthetic data | Login, fingerprint confirmation, selection, preflight, disabled/enabled Start and COPY result verified |
| Dockerfile build / container entrypoint | Passed on the GitHub Linux runner; full Docker/ESXi integration remains untested |
| GitHub build/publish workflow | `actionlint` passed; hosted results are available in [GitHub Actions](https://github.com/pokys/esxi-mover/actions) |
| Prebuilt image deployment | Mocked Docker verified the registry pull, the fallback to an image already on the host and failure when neither is available |
| Compose deployment | `compose.yaml` validated in an empty directory, including image and UUID overrides; CI runs the built image through this file |
| Launcher/OpenRC checks | `python3 scripts/test-start.py`: running daemon, selected image, OpenRC start with readiness wait, fallback to `--nodeps` when the service is blocked, daemon timeout; no networking restart in any case |
| Read-only paths against a real ESXi 6.5.0 build-5969303 host | Verified: version match, all sixteen required commands resolved, inventory with multi-line annotations, datastore table including unnamed vfat volumes, power state (LF only, confirmed with `od -c`), empty and populated snapshot trees, `No message.`, one `vmPathName` per VM |
| Real ESXi migration operations (shutdown, clone, register, rollback) | **Not run: never executed against a real host** |

Fixtures are explicitly synthetic. Fuzz seed tests run in the normal suite; this
does not claim an extended fuzzing campaign. The UI visual fixture is test-only,
binds to loopback, has no SSH backend, and is not compiled into the release binary.

## Test coverage by behavior

- VM inventory, authoritative `get.config` VMX-path agreement, datastore tables,
  unnamed VFAT/unmounted rows, explicit VMFS-5/6 allowlist, VMFS-L/unknown-format
  rejection, version families, Powered on/off/Suspended output.
- Structured VMX parsing, escaped values and unusual filenames, multiple disks,
  source immutability during rewrite, relative/absolute datastore references,
  preserved controller/identity and conservative standalone VMXF parsing.
- Thin/thick descriptor parsing, virtual capacity, allocation fallback, invalid
  descriptors, overflow, RDM, parent chains, seSparse and vmfsSparse rejection.
- Snapshot Manager, active numbered disk names, descriptor parents, orphan-looking
  deltas, stale VMSD, unknown output, and snapshots introduced after Analyze or
  during later boundaries before commit.
- COPY preserving registration and source bytes; MOVE verifying all target disks
  and configuration before unregistering source; optional target power-on.
- Every disk's verification failure, clone failure and corrupt target configuration
  preventing source unregister; target registration failure restoring source.
- Lost unregister/register replies reconciled against inventory; lost clone-launch
  reply and transient poll failure never causing a second launch.
- Dynamic moved/copy question choice numbers; target power-on failure; explicit
  registration rollback; rollback rejected while target runs.
- Graceful shutdown timeout and explicit force-off confirmation; source never
  automatically powered on; Mover appliance BIOS/SMBIOS UUID matching.
- No deletion capability in migration Host interface and no source deletion
  command primitives in the ESXi implementation.
- Actual POSIX quoting for spaces, apostrophes, quotes, dollar substitution,
  semicolons, ampersands, backticks, empty strings and newlines.
- Actual detached shell execution with mock ESXi binaries: the parent exits,
  worker completes and atomically records exit status; Powered on prevents clone.
- Actual local SSH transport: pre-auth host-key probe, pinned connection, changed
  key rejection, binary-exact configuration reads and redaction boundaries.
- Token/session checks, Secure/HttpOnly/SameSite cookies, CSRF, Origin and content
  type enforcement, invalid/oversized JSON, login throttling, no credential echo,
  no raw configuration in audit errors and concurrent session reads.

Run the suite:

```sh
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
```

Run the synthetic visual preview independently (no real credentials needed):

```sh
MOVER_VISUAL_FIXTURE=1 go test ./internal/web -run TestVisualFixture -timeout 10m
# Open http://127.0.0.1:8844. This is a UI fixture, not a working ESXi connection.
```

## First real-host test: ESXi 6.5 or 6.7

1. Use an isolated standalone lab host, two mounted VMFS datastores and a disposable
   Linux or Windows VM. Have a separate verified backup and exclusive maintenance
   access. Disable competing snapshots, backups, auto-starts and host automation.
2. Start with one small thin VMDK, no snapshots, no stale snapshot artifacts, no
   other-VM disk chains, ordinary persistent disks and readable descriptors.
3. Confirm ESXi SSH is enabled. Verify both HTTPS and SSH fingerprints using the
   appliance/host console. Check the actual command formats against fixtures.
4. Choose **COPY**. Review the report, conservative capacity requirement and source
   identity. If anything is unknown, stop and collect anonymized outputs; do not
   weaken a blocker to force the migration.
5. Observe graceful shutdown and actual Powered off. Confirm `vmkfstools` starts
   only afterward, source stays registered, and target stays unregistered.
6. Inspect target descriptors, extent sizes, thin format, controller references and
   `vmkfstools -e`. Confirm source files still exist. Compare guest-level data through
   an independent integrity check appropriate to that test VM.
7. For boot testing, manually register the copied VM through Host Client on an
   isolated network. Assign an independent copy identity if it will coexist with the
   source. Never power both copies with the same identity on the same network.
8. On a **different disposable VM**, test MOVE with Power On disabled. Confirm source
   unregister occurs only after target verification; target is registered and off.
9. Test MOVE with Power On enabled and inspect the actual moved/copy question. Confirm
   the original BIOS UUID and network identity are retained where expected.
10. Test multiple VMDKs. Then deliberately create a snapshot after Analyze and confirm
    Start blocks before shutdown/clone. Test stale VMSD and orphan delta blockers.
11. During a disposable COPY, disconnect the Mover's network briefly. Confirm the
    same remote worker continues and no duplicate worker starts. Separately restart
    the appliance; confirm new Connect/Analyze detects the retained active metadata.
12. Exercise rejected target registration and target power-on failure in the lab.
    Verify registration recovery and that no source/target files are removed.
13. Validate Docker/Alpine memory use and real BusyBox behavior (`nohup`, `find -print0`,
    `stat -c`, `du -k`, command output and SSH algorithms) on each intended ESXi build.
14. Only after these results are documented should production use be considered.

Record exact ESXi build, VMFS version, guest OS, controller type, disk layout,
commands/output formats, source/target verification and observed recovery results.
Remove identifiers and secrets before contributing fixtures. No current result in
this repository should be interpreted as a completed real-host test.
