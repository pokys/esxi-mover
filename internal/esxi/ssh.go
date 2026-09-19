package esxi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHOptions struct{ Address, User, Password, PrivateKey, Passphrase, Fingerprint string }
type SSHExecutor struct {
	options SSHOptions
	auth    []ssh.AuthMethod
}

func NewSSH(o SSHOptions) (*SSHExecutor, error) {
	if o.User == "" || o.Fingerprint == "" {
		return nil, fmt.Errorf("username and confirmed host fingerprint are required")
	}
	if _, _, e := net.SplitHostPort(o.Address); e != nil {
		return nil, fmt.Errorf("invalid SSH host/port")
	}
	var auth []ssh.AuthMethod
	if o.PrivateKey != "" {
		var signer ssh.Signer
		var e error
		if o.Passphrase != "" {
			signer, e = ssh.ParsePrivateKeyWithPassphrase([]byte(o.PrivateKey), []byte(o.Passphrase))
		} else {
			signer, e = ssh.ParsePrivateKey([]byte(o.PrivateKey))
		}
		if e != nil {
			return nil, fmt.Errorf("cannot parse SSH private key or unlock it with that passphrase")
		}
		auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else if o.Password != "" {
		auth = []ssh.AuthMethod{ssh.Password(o.Password)}
	} else {
		return nil, fmt.Errorf("SSH credentials are required")
	}
	return &SSHExecutor{o, auth}, nil
}

// ProbeHostKey aborts the handshake in the host-key callback, before any user
// authentication. Credentials are sent only on a subsequent pinned connection.
func ProbeHostKey(ctx context.Context, address string) (string, error) {
	var fp string
	stop := errors.New("host key captured")
	cfg := &ssh.ClientConfig{User: "host-key-probe", HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error { fp = ssh.FingerprintSHA256(k); return stop }}
	c, e := dial(ctx, address, cfg)
	if c != nil {
		c.Close()
	}
	if fp != "" {
		return fp, nil
	}
	if e != nil {
		return "", fmt.Errorf("SSH host-key probe failed; check host, port and SSH algorithms")
	}
	return "", fmt.Errorf("no SSH host key returned")
}
func dial(ctx context.Context, address string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, e := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if e != nil {
		return nil, e
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	cc, ch, req, e := ssh.NewClientConn(conn, address, cfg)
	close(done)
	if e != nil {
		conn.Close()
		return nil, e
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(cc, ch, req), nil
}

type limitedBuffer struct {
	mu       sync.Mutex
	b        bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	room := (2 << 20) - b.b.Len()
	if n > room {
		b.overflow = true
		p = p[:room]
	}
	_, _ = b.b.Write(p)
	return n, nil
}
func (b *limitedBuffer) value() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String(), b.overflow
}
func (s *SSHExecutor) Redact(v string) string {
	for _, secret := range []string{s.options.Password, s.options.PrivateKey, s.options.Passphrase} {
		if secret != "" {
			v = strings.ReplaceAll(v, secret, "[REDACTED]")
		}
	}
	return v
}
func (s *SSHExecutor) Run(ctx context.Context, cmd Command) (Result, error) {
	cfg := &ssh.ClientConfig{User: s.options.User, Auth: s.auth, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if subtle.ConstantTimeCompare([]byte(ssh.FingerprintSHA256(k)), []byte(s.options.Fingerprint)) != 1 {
			return fmt.Errorf("host key changed")
		}
		return nil
	}}
	c, e := dial(ctx, s.options.Address, cfg)
	if e != nil {
		return Result{ExitCode: -1}, fmt.Errorf("SSH connection failed (authentication, network, algorithm or pinned host key)")
	}
	defer c.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-done:
		}
	}()
	session, e := c.NewSession()
	if e != nil {
		return Result{ExitCode: -1}, fmt.Errorf("SSH session unavailable")
	}
	defer session.Close()
	var out, errout limitedBuffer
	session.Stdout = &out
	session.Stderr = &errout
	session.Stdin = bytes.NewReader(cmd.Input)
	e = session.Run(cmd.Script)
	stdout, a := out.value()
	stderr, b := errout.value()
	// Machine-readable output must remain exact: even a password like "on"
	// must not alter "Powered on". Redaction occurs at the audit/display boundary.
	r := Result{stdout, stderr, 0}
	if a || b {
		return Result{ExitCode: -1}, fmt.Errorf("SSH output exceeded safe limit")
	}
	if e != nil {
		var exit *ssh.ExitError
		if errors.As(e, &exit) {
			r.ExitCode = exit.ExitStatus()
			return r, nil
		}
		r.ExitCode = -1
		return r, fmt.Errorf("SSH command interrupted; remote result is unknown")
	}
	return r, nil
}
