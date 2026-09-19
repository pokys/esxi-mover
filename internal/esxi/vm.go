package esxi

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type Power string

const (
	Off       Power = "Powered off"
	On        Power = "Powered on"
	Suspended Power = "Suspended"
)

func ParsePower(s string) (Power, error) {
	found := Power("")
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || t == "Retrieved runtime info" {
			continue
		}
		switch Power(t) {
		case Off, On, Suspended:
			if found != "" {
				return "", fmt.Errorf("ambiguous power state")
			}
			found = Power(t)
		default:
			return "", fmt.Errorf("unknown power state")
		}
	}
	if found == "" {
		return "", fmt.Errorf("missing power state")
	}
	return found, nil
}
func (c *Client) Power(ctx context.Context, id int) (Power, error) {
	s, e := c.command(ctx, "power-state", "vim-cmd", "vmsvc/power.getstate", strconv.Itoa(id))
	if e != nil {
		return "", e
	}
	return ParsePower(s)
}
func (c *Client) Snapshot(ctx context.Context, id int) (string, error) {
	return c.command(ctx, "snapshot-state", "vim-cmd", "vmsvc/snapshot.get", strconv.Itoa(id))
}
func (c *Client) Shutdown(ctx context.Context, id int) error {
	_, e := c.command(ctx, "graceful-shutdown", "vim-cmd", "vmsvc/power.shutdown", strconv.Itoa(id))
	return e
}
func (c *Client) ForceOff(ctx context.Context, id int) error {
	_, e := c.command(ctx, "explicit-force-off", "vim-cmd", "vmsvc/power.off", strconv.Itoa(id))
	return e
}
func (c *Client) Unregister(ctx context.Context, id int) error {
	_, e := c.command(ctx, "unregister", "vim-cmd", "vmsvc/unregister", strconv.Itoa(id))
	return e
}
func (c *Client) Register(ctx context.Context, p string) (int, error) {
	s, e := c.command(ctx, "register", "vim-cmd", "solo/registervm", p)
	if e != nil {
		return 0, e
	}
	id, e := strconv.Atoi(strings.TrimSpace(s))
	if e != nil || id <= 0 {
		return 0, fmt.Errorf("registration result unknown; reconcile inventory")
	}
	return id, nil
}
func (c *Client) PowerOn(ctx context.Context, id int) error {
	_, e := c.command(ctx, "power-on", "vim-cmd", "vmsvc/power.on", strconv.Itoa(id))
	return e
}
func (c *Client) Message(ctx context.Context, id int) (string, error) {
	return c.command(ctx, "vm-question", "vim-cmd", "vmsvc/message", strconv.Itoa(id))
}
func (c *Client) Answer(ctx context.Context, id int, message, choice string) error {
	_, e := c.command(ctx, "answer-moved", "vim-cmd", "vmsvc/message", strconv.Itoa(id), message, choice)
	return e
}

var messageID = regexp.MustCompile(`(?m)^\s*Virtual machine message\s+(\d+)\s*:\s*$`)
var vmPathField = regexp.MustCompile(`(?m)^\s*vmPathName\s*=\s*("(?:[^"\\\r\n]|\\.)*"),?\s*$`)

func ParseVMPath(s string) (string, error) {
	matches := vmPathField.FindAllStringSubmatch(s, -1)
	if len(matches) != 1 {
		return "", fmt.Errorf("unknown or ambiguous authoritative VMX path")
	}
	p, err := strconv.Unquote(matches[0][1])
	if err != nil || ValidPath(p) != nil || !strings.HasPrefix(p, "[") || !strings.HasSuffix(p, ".vmx") {
		return "", fmt.Errorf("unsupported authoritative VMX path")
	}
	return p, nil
}
func (c *Client) VMPath(ctx context.Context, id int) (string, error) {
	s, err := c.command(ctx, "vm-identity", "vim-cmd", "vmsvc/get.config", strconv.Itoa(id))
	if err != nil {
		return "", err
	}
	return ParseVMPath(s)
}

var movedChoice = regexp.MustCompile(`(?im)^\s*(\d+)\.\s*I _?moved it\.?\s*(?:\(I _?moved it\))?\s*(?:\[default\])?\s*$`)

func MovedAnswer(s string) (string, string, error) {
	ids := messageID.FindAllStringSubmatch(s, -1)
	choices := movedChoice.FindAllStringSubmatch(s, -1)
	if len(ids) != 1 || len(choices) != 1 || !strings.Contains(s, "msg.uuid.altered") {
		return "", "", fmt.Errorf("unknown VM question; answer manually in ESXi Host Client")
	}
	return ids[0][1], choices[0][1], nil
}
