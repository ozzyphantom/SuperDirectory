package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ozzyphantom/SuperDirectory/internal/job"
)

func TestCommandLine(t *testing.T) {
	cases := []struct {
		args     []string
		code     int
		out, err string
	}{
		{[]string{"--help"}, 0, "Usage:", ""},
		{[]string{"-h"}, 0, "copy flags:", ""},
		{[]string{"--version"}, 0, "superdirectory ", ""},
		{[]string{"--frobnicate"}, 2, "", "unknown argument"},
		{[]string{"copy", "--to"}, 2, "", ""},
		{[]string{"copy", "--from", "/nope", "--to", "/tmp/x", "--yes"}, 2, "", "source"},
		{[]string{"resume"}, 2, "", "one argument"},
		{[]string{"presets", "extra"}, 2, "", "presets takes"},
	}
	for _, c := range cases {
		var out, errOut bytes.Buffer
		if got := command(c.args, &out, &errOut); got != c.code {
			t.Errorf("%v: exit %d, want %d (stderr %q)", c.args, got, c.code, errOut.String())
		}
		if !strings.Contains(out.String(), c.out) {
			t.Errorf("%v: stdout %q, want it to contain %q", c.args, out.String(), c.out)
		}
		if !strings.Contains(errOut.String(), c.err) {
			t.Errorf("%v: stderr %q, want it to contain %q", c.args, errOut.String(), c.err)
		}
	}
}

func TestParseCopyFlags(t *testing.T) {
	var errOut bytes.Buffer
	j, opt, err := parseCopy([]string{
		"--from", "a", "--from", "b,with,commas",
		"--to", "out", "--layout", "type", "--keep-folders",
		"--skip", "*.tmp", "--only", "Documents,png", "--max-size", "200MB",
		"--duplicates", "identical,pictures", "--batch", "50", "--yes",
	}, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Sources) != 2 || !filepath.IsAbs(j.Sources[0]) || !strings.HasSuffix(j.Sources[1], "b,with,commas") {
		t.Errorf("sources %v: each --from is one folder, kept whole", j.Sources)
	}
	if j.Layout != job.ByType || !j.KeepFolders || j.MaxSize != 200_000_000 || j.Batch != 50 || j.Retries != 1 {
		t.Errorf("job %+v", j)
	}
	if strings.Join(j.Only, ",") != "Documents,png" || !j.Finds(job.Pictures) || j.Finds(job.Documents) {
		t.Errorf("lists: only %v, duplicates %v", j.Only, j.Duplicates)
	}
	if !opt.yes {
		t.Error("--yes")
	}

	j, _, _ = parseCopy([]string{"--from", "a", "--to", "b", "--duplicates", "all"}, &errOut)
	if len(j.Duplicates) != 3 {
		t.Errorf("--duplicates all = %v", j.Duplicates)
	}
	if _, _, err := parseCopy([]string{"--from", "a", "--to", "b", "--min-size", "huge"}, &errOut); err == nil {
		t.Error("a bad size was accepted")
	}
}

// TestPresetThenFlags: a preset supplies the job, and only the flags actually
// given override it — an unset --batch must not reset the preset's batches to 0.
func TestPresetThenFlags(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	if err := job.SavePreset("docs", job.Job{Sources: []string{"/src"}, Layout: job.ByType, Batch: 50, Retries: 2}); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	j, _, err := parseCopy([]string{"--preset", "docs", "--to", "/out", "--verify"}, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if j.Batch != 50 || j.Layout != job.ByType || j.Retries != 2 || !j.Verify || j.Target != "/out" || j.Sources[0] != "/src" {
		t.Errorf("preset with overrides: %+v", j)
	}
}
