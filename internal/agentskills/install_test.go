package agentskills

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallCreatesClaudeAndExistingAgentsSkills(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := Install(root); err != nil {
		t.Fatal(err)
	}

	for _, base := range []string{".claude", ".agents"} {
		for _, skill := range skillNames {
			assertInstalledSkill(t, filepath.Join(root, base, "skills", skill))
		}
	}
}

func TestInstallSkipsAgentsSkillsWithoutAgentsDirectory(t *testing.T) {
	root := t.TempDir()
	if err := Install(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".agents: %v", err)
	}
}

func TestInstallAtomicallyReplacesMarkedSkill(t *testing.T) {
	root := t.TempDir()
	if err := Install(root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, ".claude", "skills", "grepnest-guide")
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Install(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "stale" || !strings.Contains(string(data), "name: grepnest-guide") {
		t.Fatalf("SKILL.md was not replaced: %q", data)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") || strings.Contains(entry.Name(), ".old-") {
			t.Fatalf("replacement artifact remains: %s", entry.Name())
		}
	}
}

func TestAtomicSwapExchangesDirectories(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	for path, content := range map[string]string{left: "left", right: "right"} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "value"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := atomicSwap(left, right); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{left: "right", right: "left"} {
		got, err := os.ReadFile(filepath.Join(path, "value"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s=%q want=%q", path, got, want)
		}
	}
}

func TestInstallRefusesUnownedSkill(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".claude", "skills", "grepnest-guide")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("user content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Install(root); !errors.Is(err, ErrUnownedDestination) {
		t.Fatalf("err=%v", err)
	}
}

func TestInstallPreflightsAllDestinations(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Install(root); err != nil {
		t.Fatal(err)
	}
	guide := filepath.Join(root, ".claude", "skills", "grepnest-guide", "SKILL.md")
	if err := os.WriteFile(guide, []byte("keep until preflight succeeds"), 0o600); err != nil {
		t.Fatal(err)
	}
	unowned := filepath.Join(root, ".agents", "skills", "grepnest-guide")
	if err := os.RemoveAll(unowned); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(unowned, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := Install(root); !errors.Is(err, ErrUnownedDestination) {
		t.Fatalf("err=%v", err)
	}
	data, err := os.ReadFile(guide)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep until preflight succeeds" {
		t.Fatalf("preflight modified another destination: %q", data)
	}
}

func TestInstallRejectsSymlinkDestinations(t *testing.T) {
	for _, test := range []struct {
		name string
		link func(root, outside string) error
	}{
		{"component", func(root, outside string) error {
			return os.Symlink(outside, filepath.Join(root, ".claude"))
		}},
		{"skill", func(root, outside string) error {
			if err := os.MkdirAll(filepath.Join(root, ".claude", "skills"), 0o700); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(root, ".claude", "skills", "grepnest-guide"))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			if err := test.link(root, outside); err != nil {
				t.Fatal(err)
			}
			if err := Install(root); !errors.Is(err, ErrUnsafeDestination) {
				t.Fatalf("err=%v", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("wrote through symlink: %v", entries)
			}
		})
	}
}

func TestInstallSkillRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	if err := installSkill(root, ".claude", "../escape"); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("escape path: %v", err)
	}
}

func assertInstalledSkill(t *testing.T, target string) {
	t.Helper()
	for _, file := range []string{"SKILL.md", markerName} {
		info, err := os.Stat(filepath.Join(target, file))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%o", file, info.Mode().Perm())
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("%s mode=%o", target, info.Mode().Perm())
	}
}
