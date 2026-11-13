package workbenchhost

// sftp_fixture_test.go — an in-process SSH server that serves the SFTP
// subsystem, rooted at a temp dir. The sshx package has the same fixture shape
// (TestSFTP*); this copy exists because the workbench host tests drive the REAL
// plugin binary against it, not the sshx client.

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func startWorkbenchSFTPServer(t *testing.T, rootDir string) (string, int) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "tester" && string(pass) == "secret" {
				return nil, nil
			}
			return nil, fmt.Errorf("invalid credentials")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveWorkbenchSFTPConn(conn, cfg, rootDir)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func serveWorkbenchSFTPConn(conn net.Conn, cfg *ssh.ServerConfig, rootDir string) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			return
		}
		go func(ch ssh.Channel, reqs <-chan *ssh.Request) {
			defer ch.Close()
			var echoStarted bool
			for req := range reqs {
				ok := false
				switch req.Type {
				case "subsystem":
					// RFC 4254: the payload is a name-prefixed string; "sftp" is
					// the one subsystem this fixture serves.
					ok = parseWorkbenchSub(req.Payload) == "sftp"
				case "pty-req", "shell", "window-change":
					ok = true
				}
				if req.WantReply {
					_ = req.Reply(ok, nil)
				}
				if req.Type == "shell" && !echoStarted && ok {
					// The provider's terminal test opens a PTY over this fixture;
					// echo everything (the PTY pump just buffers it).
					echoStarted = true
					go func() {
						buf := make([]byte, 4096)
						for {
							n, err := ch.Read(buf)
							if n > 0 {
								_, _ = ch.Write(buf[:n])
							}
							if err != nil {
								return
							}
						}
					}()
					continue
				}
				if req.Type == "subsystem" && ok {
					// Paths resolve against the server's working directory, pinned
					// to the sandbox root, so the client's "./" stays inside.
					server, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(rootDir))
					if err == nil {
						_ = server.Serve()
					}
					return
				}
			}
		}(ch, chReqs)
	}
}

func parseWorkbenchSub(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	n := int(payload[0])<<24 | int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
	if n > len(payload)-4 {
		n = len(payload) - 4
	}
	return strings.TrimSpace(string(payload[4 : 4+n]))
}
