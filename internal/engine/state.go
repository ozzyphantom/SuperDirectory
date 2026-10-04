package engine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ozzyphantom/SuperDirectory/internal/job"
)

// StateDirName is the hidden folder each superdirectory carries its run record
// and report in. The walk treats it as bookkeeping, so copying a superdirectory
// again never copies the old report into the new one.
const StateDirName = ".superdirectory"

// State is the run record kept in job.json: the settings, and whether the run
// finished. A run that never finished can be resumed with exactly its settings.
type State struct {
	Version  int       `json:"version"`
	Job      job.Job   `json:"job"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitempty"`
	Complete bool      `json:"complete"`
}

// StateDir is where a superdirectory keeps its record.
func StateDir(target string) string { return filepath.Join(target, StateDirName) }

func saveState(target string, s State) error {
	s.Version = 1
	if err := os.MkdirAll(StateDir(target), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return job.WriteFileAtomic(filepath.Join(StateDir(target), "job.json"), append(data, '\n'))
}

// LoadState reads a superdirectory's run record.
func LoadState(target string) (State, error) {
	var s State
	data, err := os.ReadFile(filepath.Join(StateDir(target), "job.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, errors.New("no SuperDirectory run record here")
		}
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, errors.New("the run record is damaged")
	}
	s.Job.Target = target // the folder may have moved since; it is where it is now
	return s, nil
}

// Interrupted reports whether target is a superdirectory whose run never finished,
// which a run into it would resume.
func Interrupted(target string) bool {
	s, err := LoadState(target)
	return err == nil && !s.Complete
}
