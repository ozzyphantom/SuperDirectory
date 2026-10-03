package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"":        0,
		"4096":    4096,
		"10KB":    10_000,
		"10 kb":   10_000,
		"1.5GB":   1_500_000_000,
		"200MB":   200_000_000,
		"200MiB":  200 << 20,
		"3k":      3000,
		"2 TB":    2_000_000_000_000,
		"512B":    512,
		" 64KiB ": 64 << 10,
	}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"ten", "-5MB", "5XB", "MB"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) accepted", bad)
		}
	}
}

func TestDatesCoverWholeDays(t *testing.T) {
	j := Job{Since: "2024-03-01", Until: "2024-03-31"}
	since, until := j.SinceTime(), j.UntilTime()
	if since.Day() != 1 || since.Hour() != 0 {
		t.Errorf("since = %v, want the start of March 1st", since)
	}
	lastMinute := time.Date(2024, 3, 31, 23, 59, 0, 0, time.Local)
	if until.Before(lastMinute) {
		t.Errorf("until = %v, want the end of March 31st", until)
	}
	if (&Job{}).UntilTime() != (time.Time{}) {
		t.Error("an unset until should be zero")
	}
}

func TestValidate(t *testing.T) {
	src := t.TempDir()
	good := func() Job { return Job{Sources: []string{src}, Target: filepath.Join(filepath.Dir(src), "out")} }
	if err := (&Job{}).Validate(); err == nil {
		t.Error("an empty job passed")
	}
	j := good()
	if err := j.Validate(); err != nil {
		t.Fatalf("a good job failed: %v", err)
	}

	bad := map[string]func(*Job){
		"relative source":      func(j *Job) { j.Sources = []string{"rel"} },
		"missing source":       func(j *Job) { j.Sources = []string{filepath.Join(src, "nope")} },
		"target inside source": func(j *Job) { j.Target = filepath.Join(src, "out") },
		"source inside target": func(j *Job) { j.Target = filepath.Dir(src) },
		"depth without depth":  func(j *Job) { j.Layout = ByDepth },
		"unknown layout":       func(j *Job) { j.Layout = "spiral" },
		"min above max":        func(j *Job) { j.MinSize, j.MaxSize = 10, 5 },
		"bad date":             func(j *Job) { j.Since = "March" },
		"until before since":   func(j *Job) { j.Since, j.Until = "2024-02-01", "2024-01-01" },
		"unknown duplicate":    func(j *Job) { j.Duplicates = []string{"cousins"} },
		"bad pattern":          func(j *Job) { j.Skip = []string{"[a-"} },
		"too many retries":     func(j *Job) { j.Retries = 99 },
	}
	for name, mutate := range bad {
		j := good()
		mutate(&j)
		if err := j.Validate(); err == nil {
			t.Errorf("%s: passed validation", name)
		}
	}
}

func TestOverlapsIgnoresCase(t *testing.T) {
	if !Overlaps("/u/o/Photos/sub", "/u/o/photos") {
		t.Error("case-only difference should overlap")
	}
	if Overlaps("/u/o/photos-super", "/u/o/photos") {
		t.Error("a sibling with a suffix is not inside")
	}
}

// usePrivateConfig points the config folder at a temporary directory on every
// platform the tests run on.
func usePrivateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("APPDATA", filepath.Join(dir, "AppData"))
}

func TestPresetsRoundTrip(t *testing.T) {
	usePrivateConfig(t)
	if names, err := Presets(); err != nil || len(names) != 0 {
		t.Fatalf("fresh config: %v, %v", names, err)
	}
	want := Job{Sources: []string{"/Volumes/Cam/DCIM"}, Layout: ByDate, Duplicates: []string{Identical, Pictures}, Batch: 50}
	if err := SavePreset("camera import", want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPreset("camera import")
	if err != nil {
		t.Fatal(err)
	}
	if got.Layout != ByDate || got.Batch != 50 || !got.Finds(Pictures) || got.Sources[0] != want.Sources[0] {
		t.Errorf("round trip changed the job: %+v", got)
	}
	if names, _ := Presets(); len(names) != 1 || names[0] != "camera import" {
		t.Errorf("Presets() = %v", names)
	}
	if err := DeletePreset("camera import"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPreset("camera import"); err == nil {
		t.Error("a deleted preset still loads")
	}
}

func TestPresetNamesStayInTheirFolder(t *testing.T) {
	for _, bad := range []string{"", "../escape", "a/b", `a\b`, ".hidden", strings.Repeat("x", 65)} {
		if ValidPresetName(bad) == nil {
			t.Errorf("%q accepted as a preset name", bad)
		}
	}
}

func TestWriteFileAtomicLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	if err := WriteFileAtomic(p, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "x.json" {
		t.Errorf("folder holds %v", entries)
	}
}
