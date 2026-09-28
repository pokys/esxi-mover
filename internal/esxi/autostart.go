package esxi

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// AutoStart is one VM's entry in the host's autostart sequence. It belongs to
// the registration, not to the VM's files, so it does not follow a VM that is
// registered again from another folder.
type AutoStart struct {
	Order  int
	Action string
}

// On reports whether the host starts the VM when it boots.
func (a AutoStart) On() bool { return a.Action == "powerOn" || a.Action == "systemDefault" }

var autoKey = regexp.MustCompile(`^key = 'vim\.VirtualMachine:(\d+)',?$`)
var autoField = regexp.MustCompile(`^(startOrder|startAction) = "?([^",]*)"?,?$`)

// ParseAutostart reads vim-cmd hostsvc/autostartmanager/get_autostartseq into
// entries by VM ID. An entry without a VM key, or two entries for one VM, is
// an error rather than a guess.
func ParseAutostart(s string) (map[int]AutoStart, error) {
	out := map[int]AutoStart{}
	id, open := 0, false
	var cur AutoStart
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "(vim.host.AutoStartManager.AutoPowerInfo) {":
			id, open, cur = 0, true, AutoStart{}
		case open && (t == "}" || t == "},"):
			if id == 0 {
				return nil, fmt.Errorf("autostart entry without a VM")
			}
			if _, dup := out[id]; dup {
				return nil, fmt.Errorf("two autostart entries for one VM")
			}
			out[id] = cur
			open = false
		case open && autoKey.MatchString(t):
			id, _ = strconv.Atoi(autoKey.FindStringSubmatch(t)[1])
		case open && autoField.MatchString(t):
			m := autoField.FindStringSubmatch(t)
			if m[1] == "startAction" {
				cur.Action = m[2]
			} else if n, e := strconv.Atoi(m[2]); e == nil {
				cur.Order = n
			}
		}
	}
	if open {
		return nil, fmt.Errorf("unfinished autostart entry")
	}
	return out, nil
}
func (c *Client) Autostart(ctx context.Context) (map[int]AutoStart, error) {
	s, e := c.command(ctx, "autostart-sequence", "vim-cmd", "hostsvc/autostartmanager/get_autostartseq")
	if e != nil {
		return nil, e
	}
	return ParseAutostart(s)
}
