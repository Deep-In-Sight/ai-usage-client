package supervisor

import (
	"bytes"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/provider"
	"github.com/Deep-In-Sight/ai-usage-client/agent/internal/trayproto"
)

// HelperArgs are every helper's arguments after the executable: the
// hidden subcommand only. What a helper is for comes in its first line
// (trayproto hello): other users of the PC can read command lines.
var HelperArgs = []string{"tray-helper"}

// ExecSpawner starts helpers as `exe tray-helper` (the agent's own
// executable, design D3) with an allow-list of the agent's environment
// (helperEnviron; keep names more, for tests) and the one-time token. The
// helper's standard error goes to lg line by line, each line starting
// with "[tray <provider>] ".
func ExecSpawner(exe string, lg *log.Logger, keep ...string) func(p provider.ID, uid uint32, token string) (Proc, error) {
	return func(p provider.ID, uid uint32, token string) (Proc, error) {
		cmd := exec.Command(exe, HelperArgs...)
		cmd.Env = append(helperEnviron(runtime.GOOS, os.Environ(), keep), trayproto.EnvToken+"="+token)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		// Not StdoutPipe: Wait would close it while the last lines may
		// still be unread.
		r, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		cmd.Stdout = w
		lw := &lineLogger{lg: lg, prefix: "[tray " + name(p) + "] "}
		cmd.Stderr = lw
		if err := cmd.Start(); err != nil {
			r.Close()
			w.Close()
			return nil, err
		}
		w.Close()
		return &execProc{cmd: cmd, stdin: stdin, stdout: r, lw: lw}, nil
	}
}

type execProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *os.File
	lw     *lineLogger
}

func (p *execProc) Stdin() io.WriteCloser { return p.stdin }
func (p *execProc) Stdout() io.Reader     { return p.stdout }
func (p *execProc) Kill()                 { _ = p.cmd.Process.Kill() }

func (p *execProc) Wait() (int, error) {
	err := p.cmd.Wait()
	p.lw.flush()
	code := -1
	if p.cmd.ProcessState != nil {
		code = p.cmd.ProcessState.ExitCode()
	}
	return code, err
}

// lineLogger logs what is written to it line by line, with a prefix; a
// line is cut at 2 KiB.
type lineLogger struct {
	mu     sync.Mutex
	lg     *log.Logger
	prefix string
	buf    []byte
}

const maxLogLine = 2 << 10

func (l *lineLogger) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, b...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			if len(l.buf) > maxLogLine {
				l.lg.Print(l.prefix + string(l.buf[:maxLogLine]))
				l.buf = l.buf[:0]
			}
			return len(b), nil
		}
		line := bytes.TrimRight(l.buf[:i], "\r")
		if len(line) > maxLogLine {
			line = line[:maxLogLine]
		}
		if len(line) > 0 {
			l.lg.Print(l.prefix + string(line))
		}
		l.buf = l.buf[i+1:]
	}
}

func (l *lineLogger) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(bytes.TrimSpace(l.buf)) > 0 {
		l.lg.Print(l.prefix + string(l.buf))
	}
	l.buf = nil
}
