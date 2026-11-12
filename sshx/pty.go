package sshx

// pty.go — an interactive PTY session over an SSH client (W-T1).
//
// A terminal channel is the plugin's answer to "give me a shell on that host".
// The mechanics are the standard SSH ones: open a "session" channel, request a
// PTY of a given size, start the shell, then pipe stdin/stdout. What this helper
// adds beyond the raw calls is the TWO close paths a terminal actually has —
//
//   the SSH session ends (network drop, server closes): the reader sees EOF and
//     the ExitCh fires, so the workbench channel can send a close frame and the
//     host's accumulator can reclaim the channel without leaking the session;
//   the CALLER closes (user closes the tab, or the plugin is being unloaded):
//     Close tears the session down and ExitCh fires too, so both paths converge.
//
// Resize is a separate request (window-change) because it happens many times
// over the session's life and must not be confused with the one-time setup.

import (
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// PTYSession is one interactive shell. It is safe for concurrent use: Write and
// Resize come from the channel's data/resize frames, the reader goroutine owns
// the output pipe.
type PTYSession struct {
	session *ssh.Session
	stdin   io.WriteCloser
	output  io.Reader

	mu       sync.Mutex
	exitErr  error
	exitOnce sync.Once
	exitCh   chan struct{}

	closeOnce sync.Once
}

// OpenPTY starts a shell with a PTY of the given size on client.
//
// The terminal type defaults to xterm-256color: every mainstream terminal
// emulator identifies as an xterm variant, and a server-side program that
// queries terminfo for an unknown type degrades to a dumb line mode — worse
// than assuming xterm.
func OpenPTY(client *ssh.Client, cols, rows uint16) (*PTYSession, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ssh: open session: %w", err)
	}
	s := &PTYSession{
		session: session,
		exitCh:  make(chan struct{}),
	}
	// The exit-status request carries the session's exit code (or a drop).
	// Capturing it turns "the session died" into an observable event both close
	// paths converge on. The client side surfaces the status through Wait()'s
	// *ssh.ExitError; a clean exit (status 0) is NOT an error, so it is recorded
	// as a normal end rather than a failure.
	go func() {
		werr := session.Wait()
		var status error
		if eerr, ok := werr.(*ssh.ExitError); ok {
			if eerr.ExitStatus() != 0 {
				status = fmt.Errorf("shell exited with status %d", eerr.ExitStatus())
			}
		} else if werr != nil {
			status = werr
		}
		s.mu.Lock()
		s.exitErr = status
		s.mu.Unlock()
		s.exitOnce.Do(func() { close(s.exitCh) })
	}()

	if err := session.RequestPty("xterm-256color", int(rows), int(cols), ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("ssh: request pty: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("ssh: stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("ssh: stdout pipe: %w", err)
	}
	s.stdin = stdin
	if err := session.Shell(); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("ssh: start shell: %w", err)
	}
	s.output = stdout
	return s, nil
}

// Write sends keystrokes (and control sequences) to the shell.
func (s *PTYSession) Write(p []byte) (int, error) {
	return s.stdin.Write(p)
}

// Resize re-announces the window size (the SSH "window-change" request).
func (s *PTYSession) Resize(cols, rows uint16) error {
	return s.session.WindowChange(int(rows), int(cols))
}

// Read reads shell output. It blocks until data is available, the session ends,
// or Close runs. io.EOF after the session ends is the normal shutdown.
func (s *PTYSession) Read(p []byte) (int, error) {
	return s.output.Read(p)
}

// ExitCh fires exactly once, on EITHER close path: the remote session ended, or
// Close ran. The channel owner waits on it to emit the close frame.
func (s *PTYSession) ExitCh() <-chan struct{} { return s.exitCh }

// ExitErr is the session's exit status once it has ended (nil before that).
func (s *PTYSession) ExitErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitErr
}

// Close tears the session down; idempotent. The exit goroutine's Wait() returns
// as a consequence, which closes ExitCh — the single convergence point.
func (s *PTYSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.session.Close()
	})
	<-s.exitCh
	return err
}
