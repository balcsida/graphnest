package agentskills

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const markerName = ".graphnest-generated"

var (
	ErrUnownedDestination = errors.New("agent skill destination is not GraphNest-owned")
	ErrUnsafeDestination  = errors.New("agent skill destination is unsafe")

	skillNames = []string{
		"graphnest-guide",
		"graphnest-exploring",
		"graphnest-debugging",
		"graphnest-impact-analysis",
	}
)

//go:embed all:assets
var assets embed.FS

func Install(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := requireDirectory(root); err != nil {
		return err
	}

	bases := []string{".claude"}
	agents := filepath.Join(root, ".agents")
	info, err := os.Lstat(agents)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: %s", ErrUnsafeDestination, agents)
		}
		bases = append(bases, ".agents")
	case !errors.Is(err, os.ErrNotExist):
		return err
	}

	for _, base := range bases {
		for _, skill := range skillNames {
			if err := preflight(root, base, skill); err != nil {
				return err
			}
		}
	}
	for _, base := range bases {
		for _, skill := range skillNames {
			if err := installSkill(root, base, skill); err != nil {
				return err
			}
		}
	}
	return nil
}

func installSkill(root, base, skill string) error {
	if !validComponent(base) || !validComponent(skill) {
		return ErrUnsafeDestination
	}
	if err := preflight(root, base, skill); err != nil {
		return err
	}
	parent := filepath.Join(root, base, "skills")
	if err := mkdirPath(root, filepath.Join(base, "skills")); err != nil {
		return err
	}

	temp, err := os.MkdirTemp(parent, "."+skill+".tmp-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(temp)
		}
	}()
	if err := writeAsset(temp, skill, "SKILL.md"); err != nil {
		return err
	}
	if err := writeAsset(temp, skill, markerName); err != nil {
		return err
	}
	if err := syncDirectory(temp); err != nil {
		return err
	}

	target := filepath.Join(parent, skill)
	if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(temp, target); err != nil {
			return err
		}
		cleanup = false
		return syncDirectory(parent)
	} else if err != nil {
		return err
	}

	if err := atomicSwap(temp, target); err != nil {
		return err
	}
	if err := syncDirectory(parent); err != nil {
		return err
	}
	if err := os.RemoveAll(temp); err != nil {
		return err
	}
	cleanup = false
	return syncDirectory(parent)
}

func preflight(root, base, skill string) error {
	if !validComponent(base) || !validComponent(skill) {
		return ErrUnsafeDestination
	}
	relative := filepath.Join(base, "skills", skill)
	if err := rejectSymlinkComponents(root, relative); err != nil {
		return err
	}
	target := filepath.Join(root, relative)
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrUnsafeDestination, target)
	}
	marker := filepath.Join(target, markerName)
	info, err = os.Lstat(marker)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrUnownedDestination, target)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", ErrUnsafeDestination, marker)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		return err
	}
	want, err := fs.ReadFile(assets, "assets/"+skill+"/"+markerName)
	if err != nil {
		return err
	}
	if string(got) != string(want) {
		return fmt.Errorf("%w: %s", ErrUnownedDestination, target)
	}
	return nil
}

func requireDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrUnsafeDestination, path)
	}
	return nil
}

func validComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value &&
		!strings.ContainsAny(value, `/\`)
}

func rejectSymlinkComponents(root, relative string) error {
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrUnsafeDestination, current)
		}
	}
	return nil
}

func mkdirPath(root, relative string) error {
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		err := os.Mkdir(current, 0o700)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: %s", ErrUnsafeDestination, current)
		}
	}
	return nil
}

func writeAsset(target, skill, name string) error {
	data, err := fs.ReadFile(assets, "assets/"+skill+"/"+name)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(target, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
