#!/bin/sh
# Capture, from one ESXi host, the output of every read-only command ESXi Mover
# parses, so the parsers can be checked against a real host of that version.
#
# It only reads: nothing is started, stopped, registered, snapshotted, copied
# or written. Names of VMs and datastores, UUIDs, IP and MAC addresses are
# replaced before anything is printed. Read the result before you send it.
#
# Run it from a machine with this repository, without copying it to the host:
#   ssh -p 22 root@ESXI_HOST sh -s -- VMID < scripts/capture-host.sh > esxi-shape.txt
# (-p is the host's SSH port.) From PowerShell, go through cmd, which passes
# the file unchanged; a PowerShell pipe would add CR to every line:
#   cmd /c "ssh -p 22 root@ESXI_HOST sh -s -- VMID < scripts\capture-host.sh > esxi-shape.txt"
# VMID is optional: a VM whose details are captured. Pick one that is running
# and has VMware Tools; without it, the first VM in the inventory is used.
set -u

tmp=/tmp/esxi-mover-capture.$$
mkdir "$tmp" || exit 1
trap 'rm -rf "$tmp"' EXIT

# Everything is written to $tmp/raw first and anonymized at the end.
raw="$tmp/raw"
: > "$raw"
section() {
  printf '\n===== %s\n' "$*" >> "$raw"
  "$@" >> "$raw" 2>&1
  printf '%s %s\n' '----- exit' "$?" >> "$raw"
}
# The same, for a command line that needs the shell (pipes).
shell_section() {
  printf '\n===== %s\n' "$1" >> "$raw"
  sh -c "$1" >> "$raw" 2>&1
  printf '%s %s\n' '----- exit' "$?" >> "$raw"
}

# Which commands exist is printed as it is: it holds only fixed command
# names, which a VM called "test" must not turn into a placeholder.
printf 'esxi-mover host capture, format 1\n'
for c in vim-cmd vmkfstools esxcli readlink find stat du nohup sh head tail mkdir mv cat test kill; do
  printf '%s: %s\n' "$c" "$(which "$c" >/dev/null 2>&1 && echo found || echo MISSING)"
done
section vmware -vl

section vim-cmd vmsvc/getallvms
section esxcli storage filesystem list
section esxcli vm process list
section vim-cmd hostsvc/autostartmanager/get_autostartseq

vmid=${1:-$(vim-cmd vmsvc/getallvms 2>/dev/null | awk '$1 ~ /^[0-9]+$/ {print $1; exit}')}
if [ -n "$vmid" ]; then
  section vim-cmd vmsvc/power.getstate "$vmid"
  section vim-cmd vmsvc/snapshot.get "$vmid"
  section vim-cmd vmsvc/message "$vmid"
  shell_section "vim-cmd vmsvc/get.guest $vmid | grep -i -E 'tools|guestState'"
  shell_section "vim-cmd vmsvc/get.config $vmid | grep -E 'vmPathName'"

  # The VM's folder, one disk descriptor and the checks run on it.
  ref=$(vim-cmd vmsvc/getallvms 2>/dev/null | awk -v id="$vmid" '$1 == id {s=$0; sub(/^[^[]*\[/, "", s); sub(/\.vmx.*/, ".vmx", s); print s; exit}')
  store=${ref%%]*}
  vmx="/vmfs/volumes/$store/${ref#*] }"
  dir=${vmx%/*}
  shell_section "readlink -f '$dir'"
  # ESXi 7 has no tr, so the listing is printed one name per line.
  shell_section "find '$dir' -mindepth 1 -maxdepth 1 -print"
  disk=$(find "$dir" -maxdepth 1 -name '*.vmdk' ! -name '*-flat.vmdk' ! -name '*-delta.vmdk' ! -name '*-sesparse.vmdk' ! -name '*-ctk.vmdk' 2>/dev/null | head -n 1)
  if [ -n "$disk" ]; then
    shell_section "head -c 4096 '$disk'"
    shell_section "stat -c %s '$disk'"
    shell_section "du -k '$disk'"
    shell_section "vmkfstools -e '$disk'"
  fi
  vmsd=$(find "$dir" -maxdepth 1 -name '*.vmsd' 2>/dev/null | head -n 1)
  [ -n "$vmsd" ] && shell_section "cat '$vmsd'"
fi

# Anonymize: names first, longest first so no name is cut inside another,
# then anything that still looks like an identifier.
pairs="$tmp/pairs"
: > "$pairs"
esxcli --formatter=csv storage filesystem list 2>/dev/null | awk -F, '
  NR == 1 { for (i = 1; i <= NF; i++) { h = tolower($i); gsub(/ /, "", h); if (h == "uuid") u = i; if (h == "volumename") v = i } next }
  v && $v != "" { n++; printf "%s\tds%d\n", $v, n; if (u && $u != "") printf "%s\tds%d-uuid\n", $u, n }
' >> "$pairs"
vim-cmd vmsvc/getallvms 2>/dev/null | awk '$1 ~ /^[0-9]+$/ {s=$0; sub(/^[0-9]+ +/, "", s); sub(/ +\[.*/, "", s); if (s != "") {n++; printf "%s\tvm%d\n", s, n}}' >> "$pairs"
h=$(hostname 2>/dev/null) && [ -n "$h" ] && printf '%s\thost\n' "$h" >> "$pairs"
awk -F '\t' '{print length($1) "\t" $0}' "$pairs" | sort -rn | cut -f2- > "$pairs.sorted"

awk -F '\t' '
  function lit(s, a, b,    i, out) {
    out = ""
    while ((i = index(s, a)) > 0) { out = out substr(s, 1, i - 1) b; s = substr(s, i + length(a)) }
    return out s
  }
  NR == FNR { if ($1 != "") { from[++n] = $1; to[n] = $2 } next }
  # Free text is where people write what must not leave the host: notes on
  # VMs, snapshot names and descriptions, file names. Only its shape is kept.
  # Headers and exit lines name paths too, so they go through the name
  # replacement below like everything else; only their text is not masked.
  { body = 1 }
  /^===== / { sec = $0; body = 0 }
  /^----- exit/ { sec = ""; body = 0 }
  body && sec ~ /getallvms/ && !/^Vmid/ {
    if (match($0, /vmx-[0-9]+/)) {
      head = substr($0, 1, RSTART + RLENGTH - 1)
      rest = substr($0, RSTART + RLENGTH)
      if (rest ~ /[^ ]/) rest = "    note"
      $0 = head rest
    } else if ($0 ~ /[^ ]/) {
      $0 = "   note"
    }
  }
  body && sec ~ /snapshot\.get/ {
    if ($0 ~ /Snapshot (Name|Desciption|Description) *:/) sub(/: .*/, ": masked")
    else if ($0 !~ /^(Get Snapshot:|\|-ROOT|-+Snapshot|[ \t]*$)/) $0 = "   masked"
  }
  body && sec ~ /\.vmsd/ && /(displayName|description) *=/ { sub(/= .*/, "= \"masked\"") }
  body && sec ~ /^===== find / && /\// {
    ext = $0; if (!sub(/.*\./, ".", ext) || ext ~ /\//) ext = ""
    $0 = "file" ext
  }
  {
    line = $0
    for (i = 1; i <= n; i++) line = lit(line, from[i], to[i])
    gsub(/[0-9a-fA-F]{8}-[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/, "00000000-00000000-0000-000000000000", line)
    gsub(/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/, "00000000-0000-0000-0000-000000000000", line)
    gsub(/([0-9a-fA-F]{2} ){7}[0-9a-fA-F]{2}-([0-9a-fA-F]{2} ){7}[0-9a-fA-F]{2}/, "56 4d 00 00 00 00 00 00-00 00 00 00 00 00 00 00", line)
    gsub(/[0-9a-fA-F]{32}/, "00000000000000000000000000000000", line)
    gsub(/([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}/, "00:00:00:00:00:00", line)
    gsub(/[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/, "192.0.2.1", line)
    print line
  }
' "$pairs.sorted" "$raw"
