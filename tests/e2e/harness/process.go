package harness

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// process is a child process whose output goes to a log file.
type process struct {
	name    string
	cmd     *exec.Cmd
	logPath string
	done    chan struct{} // closed when the process has exited
}

func startProcess(name, logDir string, env []string, bin string, args ...string) (*process, error) {
	logPath := filepath.Join(logDir, name+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log for %s: %w", name, err)
	}
	defer logFile.Close() // the child keeps its own handle

	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	p := &process{name: name, cmd: cmd, logPath: logPath, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// exited reports whether the process has stopped.
func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// kill stops the process outright, as a crash would, and waits for it to exit.
func (p *process) kill() {
	if p == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	<-p.done
}

// logTail returns the last lines of the process log, for error messages.
func (p *process) logTail(lines int) string {
	if p == nil {
		return ""
	}
	data, err := os.ReadFile(p.logPath)
	if err != nil {
		return fmt.Sprintf("(cannot read %s: %v)", p.logPath, err)
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

// freePort asks the operating system for a port nothing is listening on.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
