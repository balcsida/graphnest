package client

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoginsRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "creds")
	logins := Logins{Dir: dir}
	login := Login{Server: "https://graph.example", ClientID: "cid", AccessToken: testAccess, RefreshToken: testRefresh,
		ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second), TokenEndpoint: "https://graph.example/token", RevocationEndpoint: "https://graph.example/revoke"}
	if err := logins.Save(login); err != nil {
		t.Fatal(err)
	}
	got, ok, err := logins.Load(login.Server)
	if err != nil || !ok || !reflect.DeepEqual(got, login) {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("%v %v", entries, err)
	}
	if runtime.GOOS != "windows" {
		dirInfo, _ := os.Stat(dir)
		fileInfo, _ := entries[0].Info()
		if dirInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("modes %v %v", dirInfo.Mode().Perm(), fileInfo.Mode().Perm())
		}
	}
	if _, ok, err = logins.Load("https://other.example"); ok || err != nil {
		t.Fatalf("other origin: %v %v", ok, err)
	}
	// A file that sits under this origin's name but records another server is ignored.
	other := login
	other.Server = "https://other.example"
	if err = logins.Save(other); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(dir, loginFileName(other.Server)), filepath.Join(dir, loginFileName("https://third.example"))); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = logins.Load("https://third.example"); ok || err != nil {
		t.Fatalf("mismatched server: %v %v", ok, err)
	}
	if err = logins.Delete(login.Server); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ = logins.Load(login.Server); ok {
		t.Fatal("login survived Delete")
	}
	if err = logins.Delete(login.Server); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = (Logins{}).Load(login.Server); ok || err != nil {
		t.Fatalf("zero value: %v %v", ok, err)
	}
	if (Logins{}).Save(login) == nil {
		t.Fatal("zero value saved")
	}
}

func TestLoginsKeepOSErrorReasons(t *testing.T) {
	dir := t.TempDir()
	logins := Logins{Dir: dir}
	if err := os.WriteFile(filepath.Join(dir, loginFileName("https://h")), []byte("{not json "+testRefresh), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := logins.Load("https://h")
	noSecrets(t, err)
	if err.Error() != "login file is not valid JSON" {
		t.Fatal(err)
	}
	// A directory in place of the file makes ReadFile fail with an OS error that stays in the chain.
	if err = os.Mkdir(filepath.Join(dir, loginFileName("https://d")), 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err = logins.Load("https://d")
	var pathErr *os.PathError
	if err == nil || !errors.As(err, &pathErr) || !strings.HasPrefix(err.Error(), "login file cannot be read: ") {
		t.Fatalf("%v", err)
	}
	// A file where the directory should be makes Save fail with an OS error.
	blocked := filepath.Join(dir, "blocked")
	if err = os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err = Logins{Dir: blocked}.Save(Login{Server: "https://h", RefreshToken: testRefresh})
	if err == nil || !errors.As(err, &pathErr) || !strings.HasPrefix(err.Error(), "login directory cannot be created: ") {
		t.Fatalf("%v", err)
	}
	noSecrets(t, err)
}
