#!/usr/bin/env python3
"""Exercise the real POSIX launcher with isolated Docker and OpenRC doubles."""
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
  info)
    [ "${MOVER_TEST_RUNNING:-1}" = 1 ] && exit 0
    [ -f "$MOVER_TEST_BIN/ready" ] || exit 1
    n=0; [ ! -f "$MOVER_TEST_BIN/polls" ] || n=$(cat "$MOVER_TEST_BIN/polls")
    n=$((n + 1)); printf '%s' "$n" > "$MOVER_TEST_BIN/polls"
    [ "$n" -gt "${MOVER_TEST_READY_DELAY:-0}" ]; exit $?
    ;;
  *' up '*) printf 'image=%s uuid=%s\n' "${MOVER_IMAGE:-}" "$MOVER_APPLIANCE_UUID" >> "$MOVER_TEST_LOG" ;;
esac
exit 0
''',
    "rc-service": r'''#!/bin/sh
printf 'rc-service %s\n' "$*" >> "$MOVER_TEST_LOG"
case "$*" in
  'docker start')
    [ "${MOVER_TEST_RC_FAIL:-0}" = 0 ] || exit 1
    touch "$MOVER_TEST_BIN/ready" ;;
  'sysfs start'|'cgroups start') ;;
  '--nodeps docker start')
    [ "${MOVER_TEST_NODEPS_FAIL:-0}" = 0 ] || exit 1
    touch "$MOVER_TEST_BIN/ready" ;;
  *) exit 99 ;;
esac
''',
    "sleep": r'''#!/bin/sh
printf 'sleep %s\n' "$*" >> "$MOVER_TEST_LOG"
''',
}


class LauncherTests(unittest.TestCase):
    def run_start(self, *, success=True, **variables):
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
            result = subprocess.run(command, env=env, stdin=subprocess.DEVNULL,
                                    capture_output=True, text=True, timeout=15)
            calls = log.read_text().splitlines() if log.exists() else []
            self.assertEqual(result.returncode == 0, success, result.stderr)
            self.assertEqual(any(" up " in call for call in calls), success, calls)
            self.assertFalse(any("networking" in call or " restart" in call for call in calls), calls)
            return result, calls

    def test_running_daemon_starts_the_application_directly(self):
        _, calls = self.run_start()
        self.assertIn("image= uuid=fixture-appliance", calls)
        self.assertFalse(any("rc-service" in call for call in calls))

    def test_selected_image_reaches_the_container(self):
        _, calls = self.run_start(MOVER_IMAGE="esxi-mover:local")
        self.assertIn("image=esxi-mover:local uuid=fixture-appliance", calls)

    def test_stopped_daemon_is_started_and_awaited(self):
        _, calls = self.run_start(MOVER_TEST_RUNNING="0", MOVER_TEST_READY_DELAY="2")
        self.assertIn("rc-service docker start", calls)
        self.assertEqual(calls.count("sleep 1"), 2)
        self.assertFalse(any("--nodeps" in call for call in calls))

    def test_blocked_service_falls_back_to_starting_without_dependencies(self):
        _, calls = self.run_start(MOVER_TEST_RUNNING="0", MOVER_TEST_RC_FAIL="1")
        self.assertEqual([call for call in calls if call.startswith("rc-service")],
                         ["rc-service docker start", "rc-service sysfs start",
                          "rc-service cgroups start", "rc-service --nodeps docker start"])

    def test_unstartable_daemon_never_starts_the_application(self):
        self.run_start(success=False, MOVER_TEST_RUNNING="0", MOVER_TEST_RC_FAIL="1",
                       MOVER_TEST_NODEPS_FAIL="1")
        result, calls = self.run_start(success=False, MOVER_TEST_RUNNING="0",
                                       MOVER_TEST_READY_DELAY="99")
        self.assertIn("within 30 seconds", result.stderr)
        self.assertEqual(calls.count("sleep 1"), 30)


if __name__ == "__main__":
    unittest.main()
