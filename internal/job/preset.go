package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ConfigDir is where SuperDirectory keeps its own files: presets, and the editable
// category table. It follows the platform's convention (~/Library/Application
// Support on macOS, %AppData% on Windows, $XDG_CONFIG_HOME or ~/.config on Linux).
func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "SuperDirectory"), nil
}

// presetDir is where presets live, one JSON file each.
func presetDir() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "presets"), nil
}

// ValidPresetName rejects names that would not survive as a file name on every
// system, or that could reach outside the preset folder.
func ValidPresetName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("a preset needs a name")
	}
	if len(name) > 64 {
		return errors.New("preset names are at most 64 characters")
	}
	for _, r := range name {
		ok := r == ' ' || r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("preset names use letters, digits, spaces, '-', '_' and '.' only, not %q", r)
		}
	}
	if strings.HasPrefix(name, ".") {
		return errors.New("preset names cannot start with '.'")
	}
	return nil
}

// SavePreset stores j under name, replacing any preset of that name.
func SavePreset(name string, j Job) error {
	name = strings.TrimSpace(name)
	if err := ValidPresetName(name); err != nil {
		return err
	}
	dir, err := presetDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, name+".json"), append(data, '\n'))
}

// LoadPreset reads the preset called name.
func LoadPreset(name string) (Job, error) {
	if err := ValidPresetName(name); err != nil {
		return Job{}, err
	}
	dir, err := presetDir()
	if err != nil {
		return Job{}, err
	}
	var j Job
	data, err := os.ReadFile(filepath.Join(dir, strings.TrimSpace(name)+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return j, fmt.Errorf("no preset called %q", name)
	}
	if err != nil {
		return j, err
	}
	if err := json.Unmarshal(data, &j); err != nil {
		return j, fmt.Errorf("preset %q is damaged: %w", name, err)
	}
	return j, nil
}

// Presets lists the saved presets' names, sorted.
func Presets() ([]string, error) {
	dir, err := presetDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".json"); ok && !e.IsDir() && ValidPresetName(n) == nil {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

// DeletePreset removes a preset.
func DeletePreset(name string) error {
	if err := ValidPresetName(name); err != nil {
		return err
	}
	dir, err := presetDir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, strings.TrimSpace(name)+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no preset called %q", name)
	}
	return err
}

// writeFileAtomic writes through a temporary file and a rename, so a crash or a
// full disk never leaves a half-written preset or report behind.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// WriteFileAtomic is writeFileAtomic for other packages that keep records.
func WriteFileAtomic(path string, data []byte) error { return writeFileAtomic(path, data) }
