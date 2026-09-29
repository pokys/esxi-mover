These are synthetic, representative fixtures, not captures from a tested ESXi host.
Version/build labels identify the output families under test and do not establish
real-host compatibility. Unknown output is intentionally rejected. Contribute
anonymized real-host output only after removing names, addresses and identifiers.

## Checking another ESXi version

`scripts/capture-host.sh` prints, from one host, the output of every read-only
command ESXi Mover parses. It changes nothing on the host, and replaces the names
of VMs and datastores, UUIDs, IP and MAC addresses before printing. Run it from a
machine with this repository; nothing is copied to the host:

```sh
ssh -p 22 root@ESXI_HOST sh -s -- VMID < scripts/capture-host.sh > esxi-shape.txt
```

In PowerShell, which has no `<` redirection:

```powershell
Get-Content scripts/capture-host.sh | ssh -p 22 root@ESXI_HOST sh -s -- VMID > esxi-shape.txt
```

`-p` is the host's SSH port. `VMID` (from `vim-cmd vmsvc/getallvms`) is a VM
whose details are captured; pick a running one with VMware Tools. Read `esxi-shape.txt` before sharing it:
snapshot names and annotations are printed as they are.
