//go:build unix

package indexer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Runner struct {
	MaxOutput int64
	KillGrace time.Duration
}

func (runner Runner) Run(ctx context.Context, binary string, args, environment []string, directory string) error {
	if runner.MaxOutput <= 0 || runner.KillGrace <= 0 {
		return errors.New("runner limits must be positive")
	}
	output := &boundedWriter{remaining: runner.MaxOutput}
	command := exec.CommandContext(ctx, binary, args...)
	command.Env, command.Dir = environment, directory
	command.Stdout, command.Stderr = output, output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	killDone := make(chan struct{})
	runDone := make(chan struct{})
	var cancelled atomic.Bool
	command.Cancel = func() error {
		cancelled.Store(true)
		err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		go func(pid int) {
			defer close(killDone)
			timer := time.NewTimer(runner.KillGrace)
			defer timer.Stop()
			select {
			case <-timer.C:
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			case <-runDone:
				if syscall.Kill(-pid, 0) == nil {
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
			}
		}(command.Process.Pid)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	err := command.Run()
	close(runDone)
	if cancelled.Load() {
		<-killDone
	}
	if err == nil {
		return nil
	}
	return fmt.Errorf("run %s: %w", filepath.Base(binary), err)
}

type boundedWriter struct {
	mu        sync.Mutex
	remaining int64
}

func (writer *boundedWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	written := len(data)
	if int64(written) > writer.remaining {
		written = int(writer.remaining)
	}
	writer.remaining -= int64(written)
	return len(data), nil
}

var _ io.Writer = (*boundedWriter)(nil)
