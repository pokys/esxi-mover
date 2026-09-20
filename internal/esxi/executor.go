package esxi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Command struct {
	Category, Script string
	Input            []byte
}
type Result struct {
	Stdout, Stderr string
	ExitCode       int
}

// Executor deliberately exposes no VM/directory deletion operation.
type Executor interface {
	Run(context.Context, Command) (Result, error)
}
type Event struct {
	Time       time.Time
	Category   string
	Command    string
	DurationMS int64
	ExitCode   int
	Error      string
}
type Audit struct {
	mu     sync.Mutex
	events []Event
}

func (a *Audit) Add(e Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	if len(a.events) > 500 {
		a.events = append([]Event(nil), a.events[len(a.events)-500:]...)
	}
}
func (a *Audit) Events() []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Event(nil), a.events...)
}

// Quote is the sole POSIX shell argument quoting implementation.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }
func Argv(args ...string) string {
	q := make([]string, len(args))
	for i, s := range args {
		q[i] = Quote(s)
	}
	return strings.Join(q, " ")
}

type Client struct {
	Exec  Executor
	Audit *Audit
}

func NewClient(e Executor) *Client { return &Client{Exec: e, Audit: &Audit{}} }
func (c *Client) Redact(s string) string {
	if redactor, ok := c.Exec.(interface{ Redact(string) string }); ok {
		return redactor.Redact(s)
	}
	return s
}
func (c *Client) run(ctx context.Context, category, script string, input []byte) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	start := time.Now()
	r, e := c.Exec.Run(ctx, Command{category, script, input})
	if e == nil && r.ExitCode != 0 {
		if category == "read-file" || category == "vm-identity" {
			e = fmt.Errorf("configuration read exited with status %d", r.ExitCode)
		} else {
			e = fmt.Errorf("%s exited with status %d: %s", category, r.ExitCode, strings.TrimSpace(r.Stderr+" "+r.Stdout))
		}
	}
	if e != nil && len(e.Error()) > 1500 {
		e = fmt.Errorf("%s: remote command failed (output too long)", category)
	}
	if e != nil {
		e = fmt.Errorf("%s", c.Redact(e.Error()))
	}
	msg := ""
	if e != nil {
		msg = e.Error()
	}
	// The script itself is the part that makes a failure diagnosable. It never
	// carries credentials, but redact it like any other displayed value.
	shown := c.Redact(script)
	if len(shown) > 400 {
		shown = strings.ToValidUTF8(shown[:400], "") + " ..."
	}
	c.Audit.Add(Event{start, category, shown, time.Since(start).Milliseconds(), r.ExitCode, msg})
	return r, e
}
func (c *Client) command(ctx context.Context, cat string, args ...string) (string, error) {
	r, e := c.run(ctx, cat, Argv(args...), nil)
	return r.Stdout, e
}
func (c *Client) ReadFile(ctx context.Context, p string) (string, error) {
	// Read at most 1 MiB + 1 without ever buffering an extent file.
	r, e := c.run(ctx, "read-file", Argv("head", "-c", "1048577", p), nil)
	if e != nil {
		return "", fmt.Errorf("cannot read configuration file")
	}
	if len(r.Stdout) > 1<<20 {
		return "", fmt.Errorf("configuration file exceeds 1 MiB")
	}
	return r.Stdout, nil
}
func (c *Client) Exists(ctx context.Context, p string) (bool, error) {
	r, e := c.run(ctx, "file-exists", "if "+Argv("test", "-e", p)+"; then printf yes; else printf no; fi", nil)
	if e != nil {
		return false, e
	}
	switch r.Stdout {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, fmt.Errorf("unknown file existence response")
	}
}
func (c *Client) Canonical(ctx context.Context, p string) (string, error) {
	s, e := c.command(ctx, "canonical-path", "readlink", "-f", p)
	if e != nil {
		return "", e
	}
	s = strings.TrimSuffix(s, "\n")
	if !strings.HasPrefix(s, "/vmfs/volumes/") || strings.ContainsAny(s, "\r\n\x00") || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("path is not a canonical datastore path")
	}
	return s, nil
}
func (c *Client) List(ctx context.Context, dir string) ([]string, error) {
	s, e := c.command(ctx, "list-directory", "find", dir, "-mindepth", "1", "-maxdepth", "1", "-print0")
	if e != nil {
		return nil, e
	}
	if s == "" {
		return nil, nil
	}
	if !strings.HasSuffix(s, "\x00") {
		return nil, fmt.Errorf("unknown directory listing format")
	}
	return strings.Split(strings.TrimSuffix(s, "\x00"), "\x00"), nil
}
func (c *Client) Size(ctx context.Context, p string) (int64, error) {
	s, e := c.command(ctx, "file-size", "stat", "-c", "%s", p)
	if e != nil {
		return 0, e
	}
	return positiveNumber(s)
}
func (c *Client) Allocated(ctx context.Context, p string) (int64, error) {
	s, e := c.command(ctx, "allocated-size", "du", "-k", p)
	if e != nil {
		return 0, e
	}
	f := strings.Fields(s)
	if len(f) < 2 {
		return 0, fmt.Errorf("unknown allocated size")
	}
	n, e := positiveNumber(f[0])
	if n > 1<<52 {
		return 0, fmt.Errorf("allocated size overflow")
	}
	return n * 1024, e
}
func (c *Client) VerifyChain(ctx context.Context, p string) error {
	s, e := c.command(ctx, "verify-chain", "vmkfstools", "-e", p)
	if e != nil {
		return e
	}
	if strings.TrimSpace(s) != "Disk chain is consistent." && strings.TrimSpace(s) != "Disk chain is consistent" {
		return fmt.Errorf("unrecognized disk-chain verification response")
	}
	return nil
}
