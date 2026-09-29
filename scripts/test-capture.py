#!/usr/bin/env python3
"""Run the host capture script against fake ESXi commands whose output is full
of made-up sensitive values, and fail if any of them reaches the capture."""
from pathlib import Path
import os
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SHELL = shutil.which("sh") or r"C:\Program Files\Git\bin\sh.exe"

# Every value here is invented; each stands for something a real host holds.
SENSITIVE = ["Fake Mail", "Fake Store", "fake-2", "fakeweb", "fake-esx-host",
             "aaaaaaaa", "eeeeeeee", "203.0.113.9", "SecretPass", "SecretUpgrade",
             "SecretTicket", "SecretDesc", "SecretCustomerList", "aa bb cc dd",
             "02:00:5e:10:20:30"]

DOUBLES = {
    "vim-cmd": r'''#!/bin/sh
case "$1" in
 vmsvc/getallvms) printf 'Vmid  Name  File  Guest OS  Version  Annotation\n12     Fake Mail   [Fake Store] Fake Mail/Fake Mail.vmx   debian9_64Guest   vmx-13   root pw SecretPass1 at 203.0.113.9\n   second line SecretPass2\nthird SecretPass3\n13     fakeweb   [fake-2] fakeweb/fakeweb.vmx   ubuntu64Guest   vmx-14\n';;
 vmsvc/power.getstate) printf 'Retrieved runtime info\nPowered on\n';;
 vmsvc/snapshot.get) printf 'Get Snapshot:\n|-ROOT\n--Snapshot Name        : before SecretUpgrade\n--Snapshot Id        : 1\n--Snapshot Desciption  : ticket SecretTicket\ncontinues SecretDesc\n--Snapshot State       : powered off\n';;
 vmsvc/message) printf 'No message.\n';;
 vmsvc/get.guest) printf '   toolsRunningStatus = "guestToolsRunning", \n   ipAddress = "203.0.113.9", \n   macAddress = "02:00:5e:10:20:30", \n';;
 vmsvc/get.config) printf '      vmPathName = "[Fake Store] Fake Mail/Fake Mail.vmx", \n';;
 hostsvc/autostartmanager/get_autostartseq) printf '(vim.host.AutoStartManager.AutoPowerInfo) [\n]\n';;
esac
''',
    "esxcli": r'''#!/bin/sh
case "$*" in
 "--formatter=csv storage filesystem list") printf 'Free,MountPoint,Mounted,Size,Type,UUID,VolumeName,\n1,/vmfs/volumes/aaaaaaaa-bbbbbbbb-cccc-dddddddddddd,true,2,VMFS-6,aaaaaaaa-bbbbbbbb-cccc-dddddddddddd,Fake Store,\n1,/vmfs/volumes/eeeeeeee-ffffffff-1111-222222222222,true,2,VMFS-6,eeeeeeee-ffffffff-1111-222222222222,fake-2,\n';;
 "storage filesystem list") printf 'Mount Point  Volume Name  UUID  Mounted  Type  Size  Free\n/vmfs/volumes/aaaaaaaa-bbbbbbbb-cccc-dddddddddddd  Fake Store  aaaaaaaa-bbbbbbbb-cccc-dddddddddddd  true  VMFS-6  2  1\n';;
 "vm process list") printf 'Fake Mail\n   UUID: 56 4d aa bb cc dd ee ff-11 22 33 44 55 66 77 88\n   Display Name: Fake Mail\n   Config File: /vmfs/volumes/aaaaaaaa-bbbbbbbb-cccc-dddddddddddd/Fake Mail/Fake Mail.vmx\n';;
esac
''',
    "find": r'''#!/bin/sh
printf '/vmfs/volumes/Fake Store/Fake Mail/Fake Mail.vmx\n/vmfs/volumes/Fake Store/Fake Mail/SecretCustomerList.xlsx\n/vmfs/volumes/Fake Store/Fake Mail/Fake Mail.vmsd\n'
''',
    "vmware": "#!/bin/sh\necho 'VMware ESXi 8.0.2 build-1'\n",
    "hostname": "#!/bin/sh\necho fake-esx-host\n",
    "vmkfstools": "#!/bin/sh\necho 'Disk chain is consistent.'\n",
}


class CaptureTests(unittest.TestCase):
    def test_nothing_sensitive_reaches_the_capture(self):
        with tempfile.TemporaryDirectory(prefix="mover-capture-") as directory:
            root = Path(directory)
            for name, body in DOUBLES.items():
                path = root / name
                path.write_text(body, encoding="utf-8", newline="\n")
                path.chmod(0o755)
            command = [SHELL, "-c", 'PATH="$(cd "$1" && pwd):$PATH"; export PATH; exec sh "$2" 12',
                       "capture-test", root.as_posix(), (ROOT / "scripts" / "capture-host.sh").as_posix()]
            result = subprocess.run(command, stdin=subprocess.DEVNULL, capture_output=True,
                                    text=True, timeout=30, env=dict(os.environ))
        self.assertEqual(result.returncode, 0, result.stderr)
        out = result.stdout
        for value in SENSITIVE:
            self.assertNotIn(value, out)
        # The shapes the parsers need are still there.
        self.assertIn("vmx-13    note", out)
        self.assertIn("--Snapshot Name        : masked", out)
        self.assertIn("file.xlsx", out)
        self.assertIn('toolsRunningStatus = "guestToolsRunning"', out)
        self.assertIn("Config File: /vmfs/volumes/ds1-uuid/vm1/vm1.vmx", out)


if __name__ == "__main__":
    unittest.main()
