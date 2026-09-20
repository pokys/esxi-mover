package esxi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

func sshFixture(t *testing.T) (string, string, *atomic.Int32) {
	t.Helper()
	count := &atomic.Int32{}
	addr, fp := sshServer(t, &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		count.Add(1)
		if c.User() == "root" && string(p) == "fixture-password" {
			return nil, nil
		}
		return nil, fmt.Errorf("rejected")
	}})
	return addr, fp, count
}

// An actual local SSH server tests the transport boundary, not only a mock.
// The caller supplies the accepted authentication, so hosts that advertise
// only one method can be reproduced.
func sshServer(t *testing.T, cfg *ssh.ServerConfig) (string, string) {
	t.Helper()
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := ssh.NewSignerFromKey(key)
	if e != nil {
		t.Fatal(e)
	}
	cfg.AddHostKey(signer)
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, e := ln.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				c, channels, requests, e := ssh.NewServerConn(conn, cfg)
				if e != nil {
					return
				}
				defer c.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					ch, reqs, e := channel.Accept()
					if e != nil {
						return
					}
					go func() {
						defer ch.Close()
						for req := range reqs {
							if req.Type != "exec" {
								_ = req.Reply(false, nil)
								continue
							}
							_ = req.Reply(true, nil)
							_, _ = ch.Write([]byte("fixture-password\x00raw"))
							_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							return
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey())
}
func TestSSHProbePinsBeforeAuthentication(t *testing.T) {
	addr, want, count := sshFixture(t)
	fp, e := ProbeHostKey(context.Background(), addr)
	if e != nil || fp != want || count.Load() != 0 {
		t.Fatal("credentials sent before host key acceptance", e, count.Load())
	}
	exec, e := NewSSH(SSHOptions{Address: addr, User: "root", Password: "fixture-password", Fingerprint: fp})
	if e != nil {
		t.Fatal(e)
	}
	r, e := exec.Run(context.Background(), Command{Category: "test", Script: "anything"})
	if e != nil || r.ExitCode != 0 || r.Stdout != "fixture-password\x00raw" || strings.Contains(exec.Redact(r.Stdout), "fixture-password") {
		t.Fatal("SSH execution or display redaction failed", e)
	}
	raw, e := exec.Run(context.Background(), Command{Category: "read-file", Script: "anything"})
	if e != nil || raw.Stdout != "fixture-password\x00raw" {
		t.Fatal("binary configuration bytes altered")
	}
	before := count.Load()
	exec.options.Fingerprint = "SHA256:wrong"
	if _, e = exec.Run(context.Background(), Command{Category: "test", Script: "anything"}); e == nil || count.Load() != before {
		t.Fatal("changed host key accepted")
	}
}
func TestSSHCredentialErrorsAreGeneric(t *testing.T) {
	_, e := NewSSH(SSHOptions{Address: "127.0.0.1:22", User: "root", PrivateKey: "secret-key-data", Fingerprint: "SHA256:test"})
	if e == nil || strings.Contains(e.Error(), "secret-key-data") {
		t.Fatal("key leaked")
	}
	if _, e = NewSSH(SSHOptions{Address: "127.0.0.1:22", User: "root", Password: "secret"}); e == nil {
		t.Fatal("unpinned SSH accepted")
	}
}

// ESXi advertises "keyboard-interactive" rather than "password" on some builds,
// and a client only attempts a method the server lists.
func TestSSHPasswordReachesKeyboardInteractiveOnlyHost(t *testing.T) {
	addr, fp := sshServer(t, &ssh.ServerConfig{KeyboardInteractiveCallback: func(c ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
		answers, e := challenge("", "", []string{"Password: "}, []bool{false})
		if e != nil {
			return nil, e
		}
		if c.User() == "root" && len(answers) == 1 && answers[0] == "fixture-password" {
			return nil, nil
		}
		return nil, fmt.Errorf("rejected")
	}})
	exec, e := NewSSH(SSHOptions{Address: addr, User: "root", Password: "fixture-password", Fingerprint: fp})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = exec.Run(context.Background(), Command{Category: "test", Script: "anything"}); e != nil {
		t.Fatal("keyboard-interactive host rejected the typed password", e)
	}
}

func TestSSHFailureNamesTheCauseWithoutLeaking(t *testing.T) {
	addr, fp, _ := sshFixture(t)
	exec, e := NewSSH(SSHOptions{Address: addr, User: "root", Password: "wrong-password", Fingerprint: fp})
	if e != nil {
		t.Fatal(e)
	}
	_, e = exec.Run(context.Background(), Command{Category: "test", Script: "anything"})
	if e == nil || !strings.Contains(e.Error(), "rejected the credentials") {
		t.Fatal("authentication failure was not identified", e)
	}
	if strings.Contains(e.Error(), "wrong-password") {
		t.Fatal("password leaked into the error")
	}
	exec.options.Fingerprint = "SHA256:wrong"
	if _, e = exec.Run(context.Background(), Command{Category: "test", Script: "anything"}); e == nil ||
		!strings.Contains(e.Error(), "no longer matches the fingerprint") {
		t.Fatal("pinned host key failure was not identified", e)
	}
}

// Every command used to pay for a full handshake, which dominated an analysis
// issuing well over a hundred of them.
func TestSSHReusesOneAuthenticatedConnection(t *testing.T) {
	addr, fp, count := sshFixture(t)
	exec, e := NewSSH(SSHOptions{Address: addr, User: "root", Password: "fixture-password", Fingerprint: fp})
	if e != nil {
		t.Fatal(e)
	}
	defer exec.Close()
	for i := 0; i < 5; i++ {
		if _, e := exec.Run(context.Background(), Command{Category: "test", Script: "anything"}); e != nil {
			t.Fatal("command failed on a reused connection:", e)
		}
	}
	if n := count.Load(); n != 1 {
		t.Fatalf("expected one authentication for five commands, got %d", n)
	}
	// A changed pin must never be answered from the pool.
	before := count.Load()
	exec.options.Fingerprint = "SHA256:wrong"
	if _, e := exec.Run(context.Background(), Command{Category: "test", Script: "anything"}); e == nil {
		t.Fatal("a pooled connection served a changed host key")
	}
	if count.Load() != before {
		t.Fatal("credentials were sent to a host whose key no longer matches")
	}
}
