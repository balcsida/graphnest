// Package cli implements the graphnest command-line tool.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/graphimport"
)

// Exit codes returned by Run.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const defaultTimeout = 2 * time.Minute

const rootUsage = `usage: graphnest <command>

commands:
  version   print the graphnest version
  graph     import graph data and show graph status
`

const graphUsage = `usage: graphnest graph <command>

commands:
  import codegraph   convert a CodeGraph index and report it (--dry-run), write it (--output FILE) or publish it
  upload             publish a v2 graph artifact file
  status             show the repository and graph status held by the server
`

// Environment holds everything Run reads from outside the process.
type Environment struct {
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	Git      func(ctx context.Context, dir string, args ...string) ([]byte, error) // runs the git binary; replaceable in tests
	Now      func() time.Time
	// Sleep waits between upload attempts; it returns early with the context's error.
	Sleep func(context.Context, time.Duration) error
	// Repository reads commits and evaluates ignore rules for freshness verification.
	Repository graphimport.Git
}

// OSEnvironment is the real process environment.
func OSEnvironment() Environment {
	return Environment{
		Getenv:   os.Getenv,
		ReadFile: os.ReadFile,
		Git: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
		},
		Now:        time.Now,
		Sleep:      sleepContext,
		Repository: graphimport.ExecGit{},
	}
}

// usageError marks a mistake in how the command was invoked.
type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

// Run executes one graphnest command and returns the process exit code.
func Run(ctx context.Context, args []string, env Environment, stdout, stderr io.Writer) int {
	err := dispatch(ctx, args, env, stdout, stderr)
	var usage usageError
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return exitOK
	case errors.As(err, &usage):
		if usage.message != "" {
			fmt.Fprintln(stderr, "error:", usage.message)
		}
		return exitUsage
	default:
		fmt.Fprintln(stderr, "error:", err)
		return exitFailure
	}
}

func dispatch(ctx context.Context, args []string, env Environment, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		io.WriteString(stderr, rootUsage)
		return usageError{}
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		io.WriteString(stderr, rootUsage)
		return nil
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "graph":
		return dispatchGraph(ctx, args[1:], env, stdout, stderr)
	}
	io.WriteString(stderr, rootUsage)
	return usageError{fmt.Sprintf("unknown command %q", args[0])}
}

func dispatchGraph(ctx context.Context, args []string, env Environment, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch {
		case args[0] == "status":
			return runGraphStatus(ctx, args[1:], env, stdout, stderr)
		case args[0] == "upload":
			return runGraphUpload(ctx, args[1:], env, stdout, stderr)
		case args[0] == "import" && len(args) > 1 && args[1] == "codegraph":
			return runImportCodeGraph(ctx, args[2:], env, stdout, stderr)
		case args[0] == "-h" || args[0] == "-help" || args[0] == "--help":
			io.WriteString(stderr, graphUsage)
			return nil
		}
	}
	io.WriteString(stderr, graphUsage)
	return usageError{"unknown graph command"}
}

// newFlags builds a flag set that prints usage to stderr and returns errors instead of exiting.
func newFlags(name, summary string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(stderr, "usage: %s\n\n", summary)
		flags.PrintDefaults()
	}
	return flags
}

// parse maps flag errors to usage errors; -h stays flag.ErrHelp.
func parse(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{}
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return usageError{fmt.Sprintf("unexpected argument %q", flags.Arg(0))}
	}
	return nil
}

func writeJSON(stdout io.Writer, value any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	_, err := stdout.Write(buffer.Bytes())
	return err
}

func runVersion(args []string, stdout, stderr io.Writer) error {
	if err := parse(newFlags("version", "graphnest version", stderr), args); err != nil {
		return err
	}
	version := "dev"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = strings.TrimSpace(info.Main.Version)
	}
	return writeJSON(stdout, struct {
		Version string `json:"version"`
	}{version})
}
