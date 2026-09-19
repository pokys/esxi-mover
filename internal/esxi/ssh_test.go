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

// An actual local SSH server tests the transport boundary, not only a mock.
func sshFixture(t *testing.T) (string, string, *atomic.Int32) {
	t.Helper()
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := ssh.NewSignerFromKey(key)
	if e != nil {
		t.Fatal(e)
	}
	count := &atomic.Int32{}
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		count.Add(1)
		if c.User() == "root" && string(p) == "fixture-password" {
			return nil, nil
		}
		return nil, fmt.Errorf("rejected")
	}}
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
	return ln.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey()), count
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
