package filter

import (
	"testing"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
)

func file(name string, size int64, mod time.Time) flatten.File {
	return flatten.File{Path: "/s/" + name, Rel: name, Size: size, ModTime: mod}
}

var march = time.Date(2024, 3, 15, 12, 0, 0, 0, time.Local)

func TestKeepByTypeCategoryOrExtension(t *testing.T) {
	r := From(&job.Job{Only: []string{"Documents", ".PNG"}}, nil)
	for name, want := range map[string]bool{
		"manual.pdf": true, "notes.TXT": true, "shot.png": true, "photo.jpg": false, "song.mp3": false,
	} {
		if got := r.Keep(file(name, 10, march)); got != want {
			t.Errorf("only Documents,png: Keep(%s) = %v", name, got)
		}
	}
	r = From(&job.Job{Not: []string{"video", "iso"}}, nil)
	for name, want := range map[string]bool{"clip.mp4": false, "disk.iso": false, "a.pdf": true} {
		if got := r.Keep(file(name, 10, march)); got != want {
			t.Errorf("not video,iso: Keep(%s) = %v", name, got)
		}
	}
	// An alias counts: "jpg" also excludes .jpeg files.
	r = From(&job.Job{Not: []string{"jpg"}}, nil)
	if r.Keep(file("a.jpeg", 10, march)) {
		t.Error(".jpeg survived --not jpg")
	}
}

func TestKeepBySizeAndDate(t *testing.T) {
	r := From(&job.Job{MinSize: 100, MaxSize: 1000, Since: "2024-03-01", Until: "2024-03-31"}, nil)
	cases := []struct {
		f    flatten.File
		want bool
	}{
		{file("ok.bin", 500, march), true},
		{file("small.bin", 99, march), false},
		{file("big.bin", 1001, march), false},
		{file("edge.bin", 1000, march), true},
		{file("early.bin", 500, time.Date(2024, 2, 29, 23, 59, 0, 0, time.Local)), false},
		{file("lastday.bin", 500, time.Date(2024, 3, 31, 23, 59, 0, 0, time.Local)), true},
		{file("late.bin", 500, time.Date(2024, 4, 1, 0, 0, 1, 0, time.Local)), false},
	}
	for _, c := range cases {
		if got := r.Keep(c.f); got != c.want {
			t.Errorf("Keep(%s) = %v, want %v", c.f.Rel, got, c.want)
		}
	}
}

func TestPatternsPruneFoldersAndSkipFilesIgnoringCase(t *testing.T) {
	r := From(&job.Job{Skip: []string{"node_modules", "*.tmp", "~$*"}}, nil)
	if !r.Prune("node_modules") || !r.Prune("Node_Modules") || r.Prune("src") {
		t.Error("folder pruning by pattern")
	}
	for name, want := range map[string]bool{"a.TMP": false, "~$report.docx": false, "report.docx": true} {
		if got := r.Keep(file(name, 10, march)); got != want {
			t.Errorf("Keep(%s) = %v", name, got)
		}
	}
	if !r.Active() || From(&job.Job{}, nil).Active() {
		t.Error("Active")
	}
}
