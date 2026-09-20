#!/usr/bin/env python3
"""Exercise the real POSIX launcher with isolated Docker/OpenRC/network doubles."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
SHELL = shutil.which("sh") or r"C:\Program Files\Git\bin\sh.exe"
DOUBLES = {
    "docker": r'''#!/bin/sh
printf 'docker %s\n' "$*" >> "$MOVER_TEST_LOG"
case "$*" in
  'compose version') exit "${MOVER_TEST_COMPOSE_EXIT:-0}" ;;
  info)
    [ "${MOVER_TEST_RUNNING:-1}" = 1 ] && exit 0
    [ -f "$MOVER_TEST_BIN/ready" ] || exit 1
    n=0; [ ! -f "$MOVER_TEST_BIN/polls" ] || n=$(cat "$MOVER_TEST_BIN/polls")
    n=$((n + 1)); printf '%s' "$n" > "$MOVER_TEST_BIN/polls"
    [ "$n" -gt "${MOVER_TEST_READY_DELAY:-0}" ]; exit $?
    ;;
  'image inspect '*) [ "${MOVER_TEST_IMAGE_PRESENT:-0}" = 1 ]; exit $? ;;
  'login ghcr.io')
    [ "${MOVER_TEST_LOGIN_FAIL:-0}" = 0 ] || exit 1
    touch "$MOVER_TEST_BIN/authenticated"; exit 0 ;;
  *' pull mover')
    case "${MOVER_TEST_PULL_ERROR:-}" in
      auth|auth-always)
        if [ ! -f "$MOVER_TEST_BIN/authenticated" ] || [ "$MOVER_TEST_PULL_ERROR" = auth-always ]; then
          printf '%s\n' 'Error response from daemon: unauthorized' >&2; exit 23
        fi ;;
      network) printf '%s\n' 'TLS handshake timeout' >&2; exit 23 ;;
    esac ;;
  *' up '*) printf 'image=%s uuid=%s\n' "$MOVER_IMAGE" "$MOVER_APPLIANCE_UUID" >> "$MOVER_TEST_LOG" ;;
esac
exit 0
''',
    "rc-service": r'''#!/bin/sh
printf 'rc-service %s\n' "$*" >> "$MOVER_TEST_LOG"
case "$*" in
  'docker start')
    case "${MOVER_TEST_RC_ERROR:-}" in
      network)
        printf '%s\n' 'ifquery: could not parse /etc/network/interfaces' \
          'ERROR: cannot start docker as networking would not start' >&2
        exit 1 ;;
      other) printf '%s\n' 'ERROR: Docker storage driver failed' >&2; exit 1 ;;
      mixed)
        printf '%s\n' 'ifquery: could not parse /etc/network/interfaces' \
          'ERROR: cannot start docker as another-service would not start' >&2
        exit 1 ;;
    esac
    touch "$MOVER_TEST_BIN/ready" ;;
  'sysfs start'|'cgroups start')
    [ "${MOVER_TEST_DEP_FAIL:-}" != "$1" ] || exit 1 ;;
  '--nodeps docker start')
    [ "${MOVER_TEST_NODEPS_FAIL:-0}" = 0 ] || exit 1
    touch "$MOVER_TEST_BIN/ready" ;;
  *) exit 99 ;;
esac
''',
    "ip": r'''#!/bin/sh
printf 'ip %s\n' "$*" >> "$MOVER_TEST_LOG"
[ "${MOVER_TEST_IP_FAIL:-0}" = 0 ] || exit 1
[ "$1" = "${MOVER_TEST_IP_FAMILY:--4}" ] || exit 0
case "$*" in
  *'route show default')
    [ "${MOVER_TEST_NO_ROUTE:-0}" = 0 ] || exit 0
    printf 'default via 192.0.2.1 dev eth0%s\n' "${MOVER_TEST_ROUTE_SUFFIX:-}" ;;
  *'addr show dev eth0 up scope global')
    [ "${MOVER_TEST_NO_ADDRESS:-0}" = 0 ] || exit 0
    printf '2: eth0: <%sUP> state UP\n' "${MOVER_TEST_CARRIER:-}"
    if [ "$1" = -4 ]; then
      printf '%s\n' '    inet 192.0.2.20/24 scope global eth0'
    else
      printf '%s\n' '    inet6 2001:db8::20/64 scope global'
    fi ;;
  *) exit 99 ;;
esac
''',
    "sleep": r'''#!/bin/sh
printf 'sleep %s\n' "$*" >> "$MOVER_TEST_LOG"
''',
}


class LauncherTests(unittest.TestCase):
    def run_start(self, *, tty=False, success=True, **variables):
        with tempfile.TemporaryDirectory(prefix="mover-launcher-") as directory:
            root = Path(directory)
            for name, body in DOUBLES.items():
                path = root / name
                path.write_text(body, encoding="utf-8", newline="\n")
                path.chmod(0o755)
            log = root / "calls"
            env = {key: value for key, value in os.environ.items() if not key.startswith("MOVER_")}
            env.update(MOVER_TEST_BIN=root.as_posix(), MOVER_TEST_LOG=log.as_posix(),
                       MOVER_APPLIANCE_UUID="fixture-appliance")
            env.update(variables)
            command = [SHELL, "-c", 'PATH="$(cd "$MOVER_TEST_BIN" && pwd):$PATH"; export PATH; exec sh "$1"',
                       "launcher-test", (ROOT / "start.sh").as_posix()]
            if tty:
                if os.name == "nt":
                    self.skipTest("PTY authentication check runs on Linux CI")
                import pty
                master, slave = pty.openpty()
                try:
                    result = subprocess.run(command, env=env, stdin=slave, stdout=subprocess.PIPE,
                                            stderr=subprocess.PIPE, text=True, timeout=15)
                finally:
                    os.close(master)
                    os.close(slave)
            else:
                result = subprocess.run(command, env=env, stdin=subprocess.DEVNULL,
                                        capture_output=True, text=True, timeout=15)
            calls = log.read_text().splitlines() if log.exists() else []
            self.assertEqual(result.returncode == 0, success, result.stderr)
            self.assertEqual(any(call.startswith("docker compose ") and " up " in call for call in calls), success, calls)
            self.assertFalse(any("networking start" in call or " restart" in call for call in calls), calls)
            return result, calls

    def test_default_pulls_the_published_image(self):
        _, calls = self.run_start()
        self.assertIn("docker compose pull mover", calls)
        self.assertIn("image=ghcr.io/pokys/esxi-mover:latest uuid=fixture-appliance", calls)
        self.assertFalse(any(" build" in call or "rc-service" in call for call in calls))

    def test_selected_image_reaches_the_container(self):
        _, calls = self.run_start(MOVER_IMAGE="ghcr.io/example/mover:checked")
        self.assertIn("image=ghcr.io/example/mover:checked uuid=fixture-appliance", calls)

    def test_loaded_image_starts_without_a_reachable_registry(self):
        for image in ("", "esxi-mover:local"):
            with self.subTest(image=image):
                _, calls = self.run_start(MOVER_TEST_PULL_ERROR="network",
                                          MOVER_TEST_IMAGE_PRESENT="1", MOVER_IMAGE=image)
                expected = image or "ghcr.io/pokys/esxi-mover:latest"
                self.assertIn("docker image inspect " + expected, calls)
                self.assertIn("image=%s uuid=fixture-appliance" % expected, calls)

    def test_compose_missing_stops_before_docker_start(self):
        _, calls = self.run_start(success=False, MOVER_TEST_COMPOSE_EXIT="1")
        self.assertEqual(calls, ["docker compose version"])

    def test_normal_daemon_start_waits_for_readiness(self):
        _, calls = self.run_start(MOVER_TEST_RUNNING="0", MOVER_TEST_READY_DELAY="2")
        self.assertIn("rc-service docker start", calls)
        self.assertEqual(calls.count("sleep 1"), 2)
        self.assertFalse(any("--nodeps" in call for call in calls))

    def test_exact_network_failure_recovers_ipv4_and_ipv6(self):
        for family in ("-4", "-6"):
            with self.subTest(family=family):
                _, calls = self.run_start(MOVER_TEST_RUNNING="0", MOVER_TEST_RC_ERROR="network",
                                          MOVER_TEST_IP_FAMILY=family)
                services = [call for call in calls if call.startswith("rc-service")]
                self.assertEqual(services, ["rc-service docker start", "rc-service sysfs start",
                                           "rc-service cgroups start", "rc-service --nodeps docker start"])

    def test_other_errors_are_never_bypassed(self):
        for error in ("other", "mixed"):
            with self.subTest(error=error):
                _, calls = self.run_start(success=False, MOVER_TEST_RUNNING="0", MOVER_TEST_RC_ERROR=error)
                self.assertFalse(any("--nodeps" in call or call.startswith("ip ") for call in calls))

    def test_unconfigured_or_down_network_never_bypasses_dependencies(self):
        for variable, value in (("MOVER_TEST_NO_ROUTE", "1"), ("MOVER_TEST_NO_ADDRESS", "1"),
                                ("MOVER_TEST_IP_FAIL", "1"), ("MOVER_TEST_ROUTE_SUFFIX", " linkdown"),
                                ("MOVER_TEST_CARRIER", "NO-CARRIER,")):
            with self.subTest(variable=variable):
                _, calls = self.run_start(success=False, MOVER_TEST_RUNNING="0", MOVER_TEST_RC_ERROR="network",
                                          **{variable: value})
                self.assertFalse(any("--nodeps" in call for call in calls))

    def test_required_service_failure_stops_recovery(self):
        for service in ("sysfs", "cgroups"):
            with self.subTest(service=service):
                _, calls = self.run_start(success=False, MOVER_TEST_RUNNING="0", MOVER_TEST_RC_ERROR="network",
                                          MOVER_TEST_DEP_FAIL=service)
                self.assertFalse(any("--nodeps" in call for call in calls))

    def test_failed_recovery_or_daemon_timeout_never_starts_application(self):
        self.run_start(success=False, MOVER_TEST_RUNNING="0", MOVER_TEST_RC_ERROR="network",
                       MOVER_TEST_NODEPS_FAIL="1")
        result, calls = self.run_start(success=False, MOVER_TEST_RUNNING="0", MOVER_TEST_READY_DELAY="99")
        self.assertIn("within 30 seconds", result.stderr)
        self.assertEqual(calls.count("sleep 1"), 30)

    def test_noninteractive_auth_failure_explains_login_without_hanging(self):
        result, calls = self.run_start(success=False, MOVER_TEST_PULL_ERROR="auth")
        self.assertIn("docker login ghcr.io", result.stderr)
        self.assertNotIn("docker login ghcr.io", calls)

    def test_network_pull_failure_does_not_prompt_for_credentials(self):
        _, calls = self.run_start(success=False, MOVER_TEST_PULL_ERROR="network")
        self.assertNotIn("docker login ghcr.io", calls)

    def test_interactive_ghcr_login_retries_once(self):
        _, calls = self.run_start(tty=True, MOVER_TEST_PULL_ERROR="auth")
        self.assertEqual(calls.count("docker login ghcr.io"), 1)
        self.assertEqual(calls.count("docker compose pull mover"), 2)

    def test_failed_interactive_login_or_retry_never_starts_application(self):
        for error, login_fail in (("auth", "1"), ("auth-always", "0")):
            with self.subTest(error=error):
                _, calls = self.run_start(tty=True, success=False, MOVER_TEST_PULL_ERROR=error,
                                          MOVER_TEST_LOGIN_FAIL=login_fail)
                self.assertEqual(calls.count("docker login ghcr.io"), 1)

    def test_other_registry_never_receives_ghcr_login(self):
        _, calls = self.run_start(tty=True, success=False, MOVER_IMAGE="registry.example/mover:latest",
                                  MOVER_TEST_PULL_ERROR="auth")
        self.assertNotIn("docker login ghcr.io", calls)


if __name__ == "__main__":
    unittest.main()
