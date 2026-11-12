package sshx

// sftp.go — the SFTP file-transfer session (W-F1).
//
// Design anchors from the workbench plan:
//
//   - ACQUISITION vs EMISSION (§6.1): the plan's central move for files. Upload
//     writes to a .tmp name FIRST and renames atomically, which makes the remote
//     change an ACQUISITION — the rename either lands whole or not at all, and a
//     failed upload leaves only a .tmp the cleanup can reclaim (an inverse that
//     actually works). Download is a pure read (no remote state change at all).
//     A direct write to the target name would be an EMISSION: half-written files
//     on a dropped connection, unrevolvable.
//   - Half-finished transfers (the plan's §4 obligation #3): UploadToTmp leaves
//     the .tmp name as its return value so the CALLER (the workbench host) can
//     register the cleanup as an effect inverse — "半完成的 SFTP 传输——.tmp 残留
//     回收登记为效应逆".
//
// The SFTP subsystem needs the server to enable it (OpenSSH does by default;
// the in-process test server mounts pkg/sftp's server side over a session
// channel, which is exactly what a real sshd does).

import (
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPSession is one file-transfer session over an SSH client. It multiplexes
// with PTY sessions on the same client (SFTP opens its own channel), so a
// terminal and a file browser ride one connection.
type SFTPSession struct {
	client *sftp.Client
}

// OpenSFTP mounts the SFTP subsystem on client.
func OpenSFTP(client *ssh.Client) (*SFTPSession, error) {
	sc, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("sftp: mount subsystem: %w", err)
	}
	return &SFTPSession{client: sc}, nil
}

// SFTPEntry is one row of a directory listing, the shape the UI renders.
type SFTPEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
	IsDir   bool   `json:"isDir"`
}

// List reads a directory. The entries are sorted (dirs first, then names) so the
// panel's order is stable regardless of the server's readdir order.
func (s *SFTPSession) List(dir string) ([]SFTPEntry, error) {
	infos, err := s.client.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("sftp: list %s: %w", dir, err)
	}
	out := make([]SFTPEntry, 0, len(infos))
	for _, fi := range infos {
		out = append(out, SFTPEntry{
			Name:    fi.Name(),
			Size:    fi.Size(),
			Mode:    fi.Mode().String(),
			ModTime: fi.ModTime().Format(time.RFC3339),
			IsDir:   fi.IsDir(),
		})
	}
	// Dirs first, then alphabetical — the two-level sort a file picker expects.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j-1], out[j]
			if (a.IsDir != b.IsDir && b.IsDir) || (a.IsDir == b.IsDir && a.Name > b.Name) {
				out[j-1], out[j] = b, a
			} else {
				break
			}
		}
	}
	return out, nil
}

// Stat describes one path (the UI's file detail / existence probe).
func (s *SFTPSession) Stat(p string) (*SFTPEntry, error) {
	fi, err := s.client.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("sftp: stat %s: %w", p, err)
	}
	return &SFTPEntry{
		Name:    fi.Name(),
		Size:    fi.Size(),
		Mode:    fi.Mode().String(),
		ModTime: fi.ModTime().Format(time.RFC3339),
		IsDir:   fi.IsDir(),
	}, nil
}

// Mkdir creates a directory (parents NOT created — an accidental deep path
// should fail visibly, not build a tree the user did not ask for).
func (s *SFTPSession) Mkdir(p string) error {
	if err := s.client.Mkdir(p); err != nil {
		return fmt.Errorf("sftp: mkdir %s: %w", p, err)
	}
	return nil
}

// Rename renames (and moves) a path. It is also the COMMIT step of the atomic
// upload below.
func (s *SFTPSession) Rename(oldPath, newPath string) error {
	if err := s.client.PosixRename(oldPath, newPath); err != nil {
		// PosixRename uses the server's rename-over extension when available
		// (OpenSSH: hardlink@openssh.com via posix-rename@); fall back to the
		// strict SSH_FXP_RENAME for servers without it — overwriting is a
		// different operation there and must be spelled explicitly.
		if err2 := s.client.Rename(oldPath, newPath); err2 != nil {
			return fmt.Errorf("sftp: rename %s → %s: %w", oldPath, newPath, err)
		}
	}
	return nil
}

// DownloadToTmp streams remotePath into a LOCAL .tmp file and returns the local
// path. The caller renames (atomic on the same volume) or cleans up. Cancelling
// the context closes the transfer mid-stream — the .tmp is the residue the
// caller's inverse reclaims.
func (s *SFTPSession) DownloadToTmp(remotePath, localTmp string) (int64, error) {
	src, err := s.client.Open(remotePath)
	if err != nil {
		return 0, fmt.Errorf("sftp: open %s: %w", remotePath, err)
	}
	defer src.Close()
	dst, err := os.OpenFile(localTmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(dst, src)
	if err != nil {
		_ = dst.Close()
		return n, fmt.Errorf("sftp: download %s: %w", remotePath, err)
	}
	if err := dst.Close(); err != nil {
		return n, err
	}
	if err := src.Close(); err != nil {
		return n, err
	}
	return n, nil
}

// UploadToTmp streams a LOCAL file to remotePath+".tmp" and returns the remote
// tmp name. The caller REMOTES the rename (commit) and registers the tmp cleanup
// as an effect inverse — the acquisition discipline above.
func (s *SFTPSession) UploadToTmp(localPath, remotePath string) (string, int64, error) {
	src, err := os.Open(localPath)
	if err != nil {
		return "", 0, err
	}
	defer src.Close()
	tmp := remotePath + ".tmp"
	dst, err := s.client.Create(tmp)
	if err != nil {
		return "", 0, fmt.Errorf("sftp: create %s: %w", tmp, err)
	}
	n, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		return tmp, n, fmt.Errorf("sftp: upload %s: %w", tmp, copyErr)
	}
	if closeErr != nil {
		return tmp, n, fmt.Errorf("sftp: close %s: %w", tmp, closeErr)
	}
	return tmp, n, nil
}

// RemoveTmp deletes one .tmp residue (the effect inverse of a failed upload).
func (s *SFTPSession) RemoveTmp(tmp string) error {
	if err := s.client.Remove(tmp); err != nil {
		return fmt.Errorf("sftp: remove %s: %w", tmp, err)
	}
	return nil
}

// Remove deletes a remote file.
func (s *SFTPSession) Remove(p string) error {
	if err := s.client.Remove(p); err != nil {
		return fmt.Errorf("sftp: remove %s: %w", p, err)
	}
	return nil
}

// UploadAtomic is UploadToTmp + Rename in one call, with the tmp cleaned up on
// a failed rename. Convenience for callers that do not need the two-step shape;
// the workbench runner uses the two-step form so the host owns the inverses.
func (s *SFTPSession) UploadAtomic(localPath, remotePath string) (int64, error) {
	tmp, n, err := s.UploadToTmp(localPath, remotePath)
	if err != nil {
		_ = s.RemoveTmp(tmp) // best-effort residue reclaim
		return n, err
	}
	if err := s.Rename(tmp, remotePath); err != nil {
		_ = s.RemoveTmp(tmp)
		return n, err
	}
	return n, nil
}

// JoinPath is path.Join with SFTP semantics (servers are unix-y; forward
// slashes).
func JoinPath(dir, name string) string {
	return path.Join(dir, name)
}

// Close ends the session.
func (s *SFTPSession) Close() error {
	return s.client.Close()
}
