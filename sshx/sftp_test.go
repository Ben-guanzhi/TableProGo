package sshx

// sftp_test.go — the in-process SFTP server fixture and the transfer tests.
//
// The server is pkg/sftp's server side mounted over a session channel of the
// SAME in-process sshd the PTY tests use — byte-for-byte what a real sshd
// serves. The backend roots at a temp dir, so List/Stat/Mkdir/Rename/Upload/
// Download run against a real filesystem the test controls.
//
// The tests pin the acquisition discipline: upload lands as .tmp first, the
// rename commits it, a FAILED upload leaves only the .tmp (the residue the
// host's effect inverse reclaims), and download round-trips bytes exactly.

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// parseSubsystemName decodes RFC 4254's subsystem request payload: a
// name-prefixed string (uint32 length + bytes).
func parseSubsystemName(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	n := int(payload[0])<<24 | int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
	if n > len(payload)-4 {
		n = len(payload) - 4
	}
	return string(payload[4 : 4+n])
}

// startSFTPServer starts an in-process sshd whose session channels serve SFTP,
// rooted at rootDir. Returns host/port.
func startSFTPServer(t *testing.T, rootDir, user, password string) (string, int) {
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
			if c.User() == user && string(pass) == password {
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
			go serveSFTPConn(conn, cfg, rootDir)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func serveSFTPConn(conn net.Conn, cfg *ssh.ServerConfig, rootDir string) {
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
			for req := range reqs {
				fmt.Fprintf(os.Stderr, "sftp-fixture: request %q\n", req.Type)
				ok := false
				switch req.Type {
				case "subsystem":
					// RFC 4254: the payload is a name-prefixed string; "sftp" is
					// the one subsystem this fixture serves.
					name := parseSubsystemName(req.Payload)
					ok = name == "sftp"
				case "pty-req", "shell", "window-change", "exec":
					ok = true
				}
				if req.WantReply {
					_ = req.Reply(ok, nil)
				}
				if req.Type == "subsystem" && ok {
					// pkg/sftp's server serves the raw channel. Paths resolve
					// against the server's working directory, which we pin to the
					// sandbox root — so the client's "/" lands inside the sandbox.
					server, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(rootDir))
					if err != nil {
						fmt.Fprintf(os.Stderr, "sftp server setup: %v\n", err)
						return
					}
					if serr := server.Serve(); serr != nil {
						fmt.Fprintf(os.Stderr, "sftp server: %v\n", serr)
					}
					return
				}
			}
		}(ch, chReqs)
	}
}

func TestSFTPListStatMkdirRename(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	host, port := startSFTPServer(t, root, "tester", "secret")

	clients, err := DialChain(SSHConfig{
		Enabled: true, Host: host, Port: port, User: "tester", Password: "secret",
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer CloseChain(clients)
	sess, err := OpenSFTP(clients[len(clients)-1])
	if err != nil {
		t.Fatalf("open sftp: %v", err)
	}
	defer sess.Close()

	entries, err := sess.List("./")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	// Dirs first, then names.
	if !entries[0].IsDir || entries[0].Name != "sub" {
		t.Fatalf("first entry = %+v, want the dir", entries[0])
	}
	if entries[1].Name != "hello.txt" {
		t.Fatalf("second entry = %+v", entries[1])
	}

	st, err := sess.Stat("./hello.txt")
	if err != nil || st.Size != 2 {
		t.Fatalf("stat = %+v err=%v", st, err)
	}

	if err := sess.Mkdir("./made"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := sess.Stat("./made"); err != nil {
		t.Fatalf("mkdir did not land: %v", err)
	}

	if err := sess.Rename("./hello.txt", "./sub/hello.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := sess.Stat("./hello.txt"); err == nil {
		t.Fatal("the renamed file still exists at the old path")
	}
	if _, err := sess.Stat("./sub/hello.txt"); err != nil {
		t.Fatalf("the renamed file is missing: %v", err)
	}
}

func TestSFTPUploadDownloadRoundTripAndTmpDiscipline(t *testing.T) {
	root := t.TempDir()
	host, port := startSFTPServer(t, root, "tester", "secret")

	clients, err := DialChain(SSHConfig{
		Enabled: true, Host: host, Port: port, User: "tester", Password: "secret",
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer CloseChain(clients)
	sess, err := OpenSFTP(clients[len(clients)-1])
	if err != nil {
		t.Fatalf("open sftp: %v", err)
	}
	defer sess.Close()

	// Upload to the .tmp name: the remote target must NOT exist yet — that is
	// the acquisition discipline (commit is the rename, not the write).
	local := filepath.Join(t.TempDir(), "payload.txt")
	payload := strings.Repeat("workbench-file-transfer\n", 1000)
	if err := os.WriteFile(local, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp, n, err := sess.UploadToTmp(local, "./data.txt")
	if err != nil {
		t.Fatalf("upload to tmp: %v", err)
	}
	if !strings.HasSuffix(tmp, ".tmp") {
		t.Fatalf("tmp name = %q, want the .tmp suffix", tmp)
	}
	if n != int64(len(payload)) {
		t.Fatalf("uploaded %d bytes, want %d", n, len(payload))
	}
	if _, err := sess.Stat("./data.txt"); err == nil {
		t.Fatal("the target exists BEFORE the rename — the write was not staged")
	}
	if _, err := sess.Stat(tmp); err != nil {
		t.Fatalf("the staged tmp is missing: %v", err)
	}

	// Commit: the rename is the acquisition.
	if err := sess.Rename(tmp, "./data.txt"); err != nil {
		t.Fatalf("commit rename: %v", err)
	}
	if _, err := sess.Stat(tmp); err == nil {
		t.Fatal("the tmp survived the commit")
	}

	// Download round-trips the bytes EXACTLY.
	localBack := filepath.Join(t.TempDir(), "back.txt")
	got, err := sess.DownloadToTmp("./data.txt", localBack)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	body, _ := os.ReadFile(localBack)
	if string(body) != payload {
		t.Fatalf("round trip corrupted: got %d bytes, want %d", got, len(payload))
	}

	// A FAILED upload (local file missing) leaves no remote residue behind —
	// UploadAtomic cleans its own tmp.
	if _, err := sess.UploadAtomic(filepath.Join(t.TempDir(), "missing.bin"), "./x.bin"); err == nil {
		t.Fatal("uploading a missing local file must fail")
	}
	entries, _ := sess.List("./")
	for _, e := range entries {
		if strings.HasSuffix(e.Name, ".tmp") {
			t.Fatalf("a .tmp residue survived: %s", e.Name)
		}
	}
}
