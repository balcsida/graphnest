package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/postgres"
	"golang.org/x/term"
)

const (
	minPasswordBytes = 16
	maxPasswordBytes = 1024
)

type terminal interface {
	Fd() uintptr
	Close() error
}

type commandRuntime struct {
	getenv       func(string) string
	openTTY      func() (terminal, error)
	isTerminal   func(int) bool
	readPassword func(int) ([]byte, error)
	hashPassword func([]byte, io.Reader) (authn.PasswordCredential, error)
	apply        func(context.Context, string, string, authn.PasswordCredential, audit.Event) error
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	os.Exit(realRuntime().run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func realRuntime() commandRuntime {
	return commandRuntime{
		getenv:       os.Getenv,
		openTTY:      func() (terminal, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) },
		isTerminal:   term.IsTerminal,
		readPassword: term.ReadPassword,
		hashPassword: authn.HashPassword,
		apply: func(ctx context.Context, databaseURL, userName string, credential authn.PasswordCredential, event audit.Event) error {
			pool, err := postgres.Open(ctx, databaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			if err := postgres.Migrate(ctx, pool); err != nil {
				return err
			}
			_, err = postgres.New(pool).UpsertBreakGlassAdmin(ctx, userName, credential, event)
			return err
		},
	}
}

func (runtime commandRuntime) run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 3 || args[0] != "break-glass" || args[1] != "set-password" ||
		args[2] == "" || strings.HasPrefix(args[2], "-") {
		fmt.Fprintln(stderr, "Usage: graphnest-admin break-glass set-password USERNAME")
		return 2
	}
	databaseURL := runtime.getenv("GRAPHNEST_DATABASE_URL")
	if !validDatabaseURL(databaseURL) {
		fmt.Fprintln(stderr, "Configuration is invalid.")
		return 1
	}
	first, second, err := runtime.passwords(stdin, stderr)
	defer clear(first)
	defer clear(second)
	if err != nil {
		fmt.Fprintln(stderr, "Could not read password.")
		return 1
	}
	if len(first) < minPasswordBytes || len(first) > maxPasswordBytes ||
		len(second) < minPasswordBytes || len(second) > maxPasswordBytes {
		fmt.Fprintln(stderr, "Password must be 16 to 1024 bytes.")
		return 1
	}
	firstHash, secondHash := sha256.Sum256(first), sha256.Sum256(second)
	matched := subtle.ConstantTimeCompare(firstHash[:], secondHash[:])
	clear(firstHash[:])
	clear(secondHash[:])
	clear(second)
	if matched != 1 {
		fmt.Fprintln(stderr, "Password entries did not match.")
		return 1
	}
	credential, err := runtime.hashPassword(first, nil)
	if err != nil {
		fmt.Fprintln(stderr, "Could not update break-glass password.")
		return 1
	}
	defer clear(credential.Salt)
	defer clear(credential.Hash)
	credential.ForceRotation = true
	event := audit.Event{
		ActorType: "operator", ActorID: "graphnest-admin", TargetType: "user",
		TargetID: auditTarget(args[2]), AuthenticationMethod: "operator",
		Operation: "break_glass_password_set", Outcome: "success",
	}
	if err := runtime.apply(ctx, databaseURL, args[2], credential, event); err != nil {
		fmt.Fprintln(stderr, "Could not update break-glass password.")
		return 1
	}
	fmt.Fprintln(stdout, "Break-glass password updated.")
	return 0
}

func (runtime commandRuntime) passwords(stdin io.Reader, stderr io.Writer) ([]byte, []byte, error) {
	tty, err := runtime.openTTY()
	if err == nil {
		defer tty.Close()
		if runtime.isTerminal(int(tty.Fd())) {
			fmt.Fprint(stderr, "New password: ")
			first, firstErr := runtime.readPassword(int(tty.Fd()))
			fmt.Fprint(stderr, "\nConfirm password: ")
			second, secondErr := runtime.readPassword(int(tty.Fd()))
			fmt.Fprintln(stderr)
			return first, second, errors.Join(firstErr, secondErr)
		}
	}
	reader := bufio.NewReaderSize(stdin, maxPasswordBytes+2)
	first, err := readPasswordLine(reader)
	if err != nil {
		return first, nil, err
	}
	second, err := readPasswordLine(reader)
	if err != nil {
		return first, second, err
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		return first, second, errors.New("unexpected password input")
	}
	return first, second, nil
}

func readPasswordLine(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if err != nil {
		clear(line)
		return nil, errors.New("invalid password input")
	}
	defer clear(line)
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) > maxPasswordBytes {
		return nil, errors.New("password too long")
	}
	return bytes.Clone(line), nil
}

func validDatabaseURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql")
}

func auditTarget(userName string) string {
	if len(userName) <= 128 {
		return userName
	}
	sum := sha256.Sum256([]byte(userName))
	return hex.EncodeToString(sum[:])
}
