package client

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Login is a stored OAuth sign-in for one GraphNest server.
type Login struct {
	Server             string    `json:"server"`
	ClientID           string    `json:"client_id"`
	AccessToken        string    `json:"access_token"`
	RefreshToken       string    `json:"refresh_token"`
	ExpiresAt          time.Time `json:"expires_at"`
	TokenEndpoint      string    `json:"token_endpoint"`
	RevocationEndpoint string    `json:"revocation_endpoint,omitempty"`
}

// Logins stores one <hex sha256(origin)>.json file per server in Dir. The zero value stores nothing.
type Logins struct{ Dir string }

func loginFileName(origin string) string {
	sum := sha256.Sum256([]byte(origin))
	return hex.EncodeToString(sum[:]) + ".json"
}

// Load returns the stored login for origin. A missing file, an empty Dir or a file recording another server is not found.
// Errors never contain file contents.
func (l Logins) Load(origin string) (Login, bool, error) {
	if l.Dir == "" {
		return Login{}, false, nil
	}
	data, err := os.ReadFile(filepath.Join(l.Dir, loginFileName(origin)))
	if errors.Is(err, os.ErrNotExist) {
		return Login{}, false, nil
	}
	if err != nil {
		return Login{}, false, errors.New("login file cannot be read")
	}
	var login Login
	if json.Unmarshal(data, &login) != nil {
		return Login{}, false, errors.New("login file is not valid JSON")
	}
	if login.Server != origin {
		return Login{}, false, nil
	}
	return login, true, nil
}

// Save writes login atomically: a synced 0600 temporary file renamed into a 0700 directory.
func (l Logins) Save(login Login) error {
	if l.Dir == "" {
		return errors.New("no login directory is available")
	}
	data, err := json.Marshal(login)
	if err != nil {
		return errors.New("login cannot be encoded")
	}
	if err = os.MkdirAll(l.Dir, 0o700); err != nil {
		return errors.New("login directory cannot be created")
	}
	temp, err := os.CreateTemp(l.Dir, ".login-*")
	if err != nil {
		return errors.New("login file cannot be written")
	}
	defer os.Remove(temp.Name()) // no-op after a successful rename
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), filepath.Join(l.Dir, loginFileName(login.Server)))
	}
	if err != nil {
		return errors.New("login file cannot be written")
	}
	return nil
}

// Delete removes the stored login for origin; a missing file is not an error.
func (l Logins) Delete(origin string) error {
	if l.Dir == "" {
		return nil
	}
	if err := os.Remove(filepath.Join(l.Dir, loginFileName(origin))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("login file cannot be removed")
	}
	return nil
}
