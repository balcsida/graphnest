package client

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
