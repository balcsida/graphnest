//go:build unix

package indexer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunnerBoundsOutputAndPreservesExit(t *testing.T) {
	runner := Runner{MaxOutput: 32, KillGrace: 100 * time.Millisecond}
	err := runner.Run(t.Context(), "/bin/sh", []string{"-c", "printf '%04096d' 0; printf secret-output >&2; exit 7"}, []string{"LANG=C"}, "")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "secret-output") {
		t.Fatalf("error exposed child output: %v", err)
	}
}

func TestRunnerOutputReturnsBoundedStdout(t *testing.T) {
	var output bytes.Buffer
	runner := Runner{MaxOutput: 4, KillGrace: 100 * time.Millisecond, Capture: &output}
	err := runner.Run(t.Context(), "/bin/sh", []string{"-c", "printf test"}, []string{"LANG=C"}, "")
	if err != nil || output.String() != "test" {
		t.Fatalf("Run() = %q, %v", output.String(), err)
	}
	if err := runner.Run(t.Context(), "/bin/sh", []string{"-c", "printf tests"}, []string{"LANG=C"}, ""); err == nil {
		t.Fatal("Run() accepted oversized stdout")
	}
}

func TestRunnerTerminatesProcessGroup(t *testing.T) {
	if os.Getenv("GREPNEST_RUNNER_HELPER") != "" {
		runnerHelper()
		return
	}
	directory := t.TempDir()
	pidFile, termFile := directory+"/pid", directory+"/term"
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	runner := Runner{MaxOutput: 64, KillGrace: 100 * time.Millisecond}
	err := runner.Run(ctx, os.Args[0], []string{"-test.run=^TestRunnerTerminatesProcessGroup$"}, []string{
		"GREPNEST_RUNNER_HELPER=parent", "GREPNEST_RUNNER_PID=" + pidFile, "GREPNEST_RUNNER_TERM=" + termFile,
	}, "")
	var exit *exec.ExitError
	if !errors.As(err, &exit) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(termFile); err != nil {
		t.Fatalf("TERM was not observed: %v", err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for process.Signal(syscall.Signal(0)) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("grandchild still alive: pid=%d err=%v", pid, err)
	}
}

func TestRunnerReturnsWhenTermEndsProcess(t *testing.T) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	err := (Runner{MaxOutput: 64, KillGrace: 5 * time.Second}).Run(ctx, os.Args[0], []string{"-test.run=^TestRunnerTerminatesProcessGroup$"}, []string{
		"GREPNEST_RUNNER_HELPER=term-exit", "GREPNEST_RUNNER_TERM=" + t.TempDir() + "/term",
	}, "")
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("TERM-responsive process waited for KILL grace: %v", elapsed)
	}
}

func runnerHelper() {
	if os.Getenv("GREPNEST_RUNNER_HELPER") == "parent" {
		command := exec.Command(os.Args[0], "-test.run=^TestRunnerTerminatesProcessGroup$")
		command.Env = []string{
			"GREPNEST_RUNNER_HELPER=grandchild",
			"GREPNEST_RUNNER_PID=" + os.Getenv("GREPNEST_RUNNER_PID"),
			"GREPNEST_RUNNER_TERM=" + os.Getenv("GREPNEST_RUNNER_TERM"),
		}
		if err := command.Start(); err != nil {
			panic(err)
		}
	}
	if os.Getenv("GREPNEST_RUNNER_HELPER") == "grandchild" {
		if err := os.WriteFile(os.Getenv("GREPNEST_RUNNER_PID"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			panic(err)
		}
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	for {
		select {
		case <-signals:
			_ = os.WriteFile(os.Getenv("GREPNEST_RUNNER_TERM"), []byte("term"), 0o600)
			if mode := os.Getenv("GREPNEST_RUNNER_HELPER"); mode == "parent" || mode == "term-exit" {
				os.Exit(0)
			}
		case <-time.After(10 * time.Millisecond):
			fmt.Print(strings.Repeat("x", 4096))
		}
	}
}
