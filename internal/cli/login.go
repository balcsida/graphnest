package cli

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/balcsida/graphnest/internal/client"
)

const callbackPath = "/callback"

// storedLogins is the login store under the user configuration directory; without one nothing is stored.
func storedLogins(env Environment) client.Logins {
	if env.ConfigDir == nil {
		return client.Logins{}
	}
	dir, err := env.ConfigDir()
	if err != nil {
		return client.Logins{}
	}
	return client.Logins{Dir: filepath.Join(dir, "graphnest", "credentials")}
}

// loginStore is like storedLogins but a missing configuration directory is an error.
func loginStore(env Environment) (client.Logins, error) {
	logins := storedLogins(env)
	if logins.Dir == "" {
		return logins, errors.New("the user configuration directory is unavailable, so logins cannot be stored")
	}
	return logins, nil
}

// callbackResult is what the browser brought back to the loopback redirect.
type callbackResult struct{ code, errCode, errDescription string }

func runLogin(ctx context.Context, args []string, env Environment, stdout, stderr io.Writer) error {
	flags := newFlags("login", "graphnest login [--timeout D]", stderr)
	timeout := flags.Duration("timeout", 5*time.Minute, "time to wait for the browser sign-in")
	if err := parse(flags, args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return usageError{"--timeout must be positive"}
	}
	logins, err := loginStore(env)
	if err != nil {
		return err
	}
	config, err := client.ServerFromEnv(env.Getenv, env.ReadFile)
	if err != nil {
		return err
	}
	origin, err := client.Origin(config.ServerURL)
	if err != nil {
		return err
	}
	oauth, err := client.DiscoverOAuth(ctx, config)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if listener, err = net.Listen("tcp", "[::1]:0"); err != nil {
			return errors.New("cannot listen on a loopback address for the sign-in redirect")
		}
	}
	defer listener.Close()
	redirectURI := "http://" + listener.Addr().String() + callbackPath
	clientID, err := oauth.Register(ctx, redirectURI)
	if err != nil {
		return err
	}
	verifier, state := client.NewSecret(), client.NewSecret()

	results := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) != 1 {
			http.Error(w, "This response does not belong to the running graphnest login.", http.StatusBadRequest)
			return
		}
		result := callbackResult{code: query.Get("code"), errCode: query.Get("error"), errDescription: query.Get("error_description")}
		if result.code == "" && result.errCode == "" {
			result.errCode = "invalid_response"
		}
		if result.errCode != "" {
			http.Error(w, "Sign-in failed. Return to the terminal for details.", http.StatusBadRequest)
		} else {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, "Signed in to GraphNest. You can close this tab and return to the terminal.\n")
		}
		select {
		case results <- result:
		default:
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(listener)

	authURL := oauth.AuthorizationURL(clientID, redirectURI, state, verifier)
	fmt.Fprintf(stderr, "Sign in to %s in your browser. If it does not open, visit:\n%s\n", origin, authURL)
	if env.OpenBrowser != nil {
		// The URL is already printed, so a failed opener is not fatal.
		if err := env.OpenBrowser(authURL); err != nil {
			fmt.Fprintln(stderr, "note: the browser could not be opened:", err)
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var result callbackResult
	select {
	case result = <-results:
	case <-waitCtx.Done():
		server.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("timed out waiting for the browser sign-in")
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	server.Shutdown(shutdownCtx)
	stop()
	listener.Close()
	if result.errCode != "" {
		if result.errDescription == "" {
			return fmt.Errorf("authorization failed: %s", result.errCode)
		}
		return fmt.Errorf("authorization failed: %s: %s", result.errCode, result.errDescription)
	}

	login, err := oauth.Exchange(ctx, clientID, redirectURI, result.code, verifier)
	if err != nil {
		return err
	}
	previous, hadPrevious, loadErr := logins.Load(origin)
	if err := logins.Save(login); err != nil {
		if revokeErr := client.RevokeLogin(ctx, config, login); revokeErr != nil {
			fmt.Fprintln(stderr, "warning: the new grant could not be revoked:", revokeErr)
		}
		return err
	}
	if env.Getenv("GRAPHNEST_TOKEN") != "" || env.Getenv("GRAPHNEST_TOKEN_FILE") != "" {
		fmt.Fprintln(stderr, "note: GRAPHNEST_TOKEN or GRAPHNEST_TOKEN_FILE is set and takes precedence over this login")
	}
	if loadErr == nil && hadPrevious {
		if err := client.RevokeLogin(ctx, config, previous); err != nil {
			fmt.Fprintln(stderr, "warning: the previous login could not be revoked:", err)
		}
	}
	fmt.Fprintf(stderr, "Signed in to %s.\n", origin)
	return nil
}

func runLogout(ctx context.Context, args []string, env Environment, stdout, stderr io.Writer) error {
	if err := parse(newFlags("logout", "graphnest logout", stderr), args); err != nil {
		return err
	}
	logins, err := loginStore(env)
	if err != nil {
		return err
	}
	config, err := client.ServerFromEnv(env.Getenv, env.ReadFile)
	if err != nil {
		return err
	}
	origin, err := client.Origin(config.ServerURL)
	if err != nil {
		return err
	}
	login, found, err := logins.Load(origin)
	if err != nil {
		return err
	}
	if !found {
		fmt.Fprintf(stderr, "Not signed in to %s.\n", origin)
		return nil
	}
	revokeErr := client.RevokeLogin(ctx, config, login)
	if err := logins.Delete(origin); err != nil {
		return err
	}
	if revokeErr != nil {
		return fmt.Errorf(`signed out locally, but the server did not revoke the grant: %w; disconnect "graphnest CLI" under Account → Connected MCP clients`, revokeErr)
	}
	fmt.Fprintf(stderr, "Signed out of %s.\n", origin)
	return nil
}
