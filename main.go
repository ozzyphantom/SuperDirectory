// SuperDirectory builds a "superdirectory" from nested trees: one flat folder, a
// folder per file type, a folder per month, or the top folders kept with
// everything below them flattened.
//
// The code is layered:
//  1. The core — walk, planners, copier (flatten, organize), the duplicate scans
//     (dedup), the readers of what files say about themselves (exif and friends).
//  2. The engine, which runs a job through every stage and draws nothing.
//  3. Two front ends over it: the wizard with its screens (wizard, ui), and the
//     command line (this file, ui.Plain).
//
// Run the wizard:   go run .
// Copy from flags:  go run . copy --from DIR --to DIR [flags]
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"

	"github.com/ozzyphantom/SuperDirectory/internal/engine"
	"github.com/ozzyphantom/SuperDirectory/internal/extract"
	"github.com/ozzyphantom/SuperDirectory/internal/job"
	"github.com/ozzyphantom/SuperDirectory/internal/notify"
	"github.com/ozzyphantom/SuperDirectory/internal/organize"
	"github.com/ozzyphantom/SuperDirectory/internal/ui"
	"github.com/ozzyphantom/SuperDirectory/internal/wizard"
)

// version is stamped at release: go build -ldflags "-X main.version=1.2.0".
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		os.Exit(command(os.Args[1:], os.Stdout, os.Stderr))
	}
	// The wizard draws on stdout and stderr and reads keys from stdin. Without a
	// terminal on all three, every screen failed at once and the app reported a
	// clean exit, having done nothing and said nothing.
	if !interactive() {
		fmt.Fprintln(os.Stderr, "superdirectory: the wizard needs a terminal. Run it in one, or see --help.")
		os.Exit(1)
	}
	os.Exit(runWizard())
}

func interactive() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		if !term.IsTerminal(f.Fd()) {
			return false
		}
	}
	return true
}

const usage = `SuperDirectory — copy nested folders into one superdirectory: flat, by type,
by date taken, or with the top folders kept.

Usage:
  superdirectory                     start the wizard
  superdirectory copy [flags]        copy without the wizard
  superdirectory resume TARGET       finish an interrupted copy into TARGET
  superdirectory presets             list saved presets; presets delete NAME removes one
  superdirectory categories          print the type categories; --write saves them for editing
  superdirectory inspect DIR         show each file's detected type and title
  superdirectory --help | --version

copy flags:
  --from DIR          a source folder (repeat for several)
  --to DIR            the folder to create
  --layout NAME       flat (default), type, date, or depth
  --depth N           with --layout depth: keep the top N folder levels
  --keep-folders      with --layout type: keep the original folders inside each type folder
  --exclude DIR       skip a folder (repeat)
  --skip PATTERN      skip names that match, e.g. '*.tmp' or node_modules (repeat)
  --only LIST         copy only these categories or extensions, e.g. Documents,png
  --not LIST          never copy these categories or extensions
  --min-size SIZE     skip files smaller than SIZE, e.g. 10KB
  --max-size SIZE     skip files larger than SIZE, e.g. 200MB
  --since DATE        skip files modified before DATE (YYYY-MM-DD)
  --until DATE        skip files modified after DATE
  --duplicates LIST   off, or any of identical,pictures,documents; all for every kind
  --review            review duplicate sets one by one before skipping
  --rename-titles     name documents after their own titles
  --detect-types      sort and name files by their content, not their extension
  --expand            expand .zip, .tar and .chm files found in the sources
  --merge-text        merge text documents into files of at most 400,000 words
  --batch N           split the output into folders of at most N files
  --verify            re-read every copy and compare it with its source
  --retries N         retry failed files N times (default 1)
  --no-report         do not write the report into TARGET/.superdirectory
  --notify            show a desktop notification when the copy finishes
  --preset NAME       start from a saved preset; flags override it
  --save-preset NAME  save these settings as a preset
  --yes               do not ask before copying

Exit status: 0 done, 1 an error or files that failed, 2 bad usage, 130 stopped.
`

// command runs a non-wizard invocation and returns the exit code.
func command(args []string, out, errOut io.Writer) int {
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(out, usage)
		return 0
	case "-v", "--version", "version":
		fmt.Fprintln(out, "superdirectory", resolvedVersion())
		return 0
	case "copy":
		return copyCommand(args[1:], errOut)
	case "resume":
		return resumeCommand(args[1:], errOut)
	case "presets":
		return presetsCommand(args[1:], out, errOut)
	case "categories":
		return categoriesCommand(args[1:], out, errOut)
	case "inspect":
		return inspectCommand(args[1:], out, errOut)
	default:
		fmt.Fprintf(errOut, "superdirectory: unknown argument %q\n\n%s", args[0], usage)
		return 2
	}
}

// resolvedVersion is the stamped release version or, for a build from `go install
// …@v1.2.0` or a git checkout, the module version Go recorded in the binary.
func resolvedVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

// ── the wizard ─────────────────────────────────────────────────────────────

func runWizard() int {
	fmt.Println()
	fmt.Println("  " + ui.Cyan.Render("SuperDirectory"))
	fmt.Println("  " + ui.Dim.Render("Copy nested folders into one superdirectory.  ") + ui.Key.Render("Ctrl+C") + ui.Dim.Render(" exits anytime."))
	for {
		j, err := wizard.Run()
		if err != nil {
			if wizard.IsAbort(err) {
				fmt.Println("\n  Exiting.")
				return 0
			}
			fmt.Fprintln(os.Stderr, "\n  "+ui.Red.Render("Error: ")+err.Error())
			return 1
		}
		fmt.Println()
		stop := ui.NewStopper()
		release := stop.Catch()
		hooks := &ui.Interactive{Stop: stop, Review: reviewHook()}
		sum, err := engine.Run(j, hooks, stop.C())
		release()

		switch {
		case errors.Is(err, engine.ErrAbandoned):
			fmt.Println("\n  Exiting.")
			return 0
		case errors.Is(err, engine.ErrStopped):
			ui.PrintSummary(os.Stdout, sum)
			return 130 // the shell convention for a run stopped by Ctrl+C
		case err != nil:
			fmt.Fprintln(os.Stderr, "\n  "+ui.Red.Render("Error: ")+err.Error())
			return 1
		}
		ui.PrintSummary(os.Stdout, sum)
		sendNotification(j, sum)
		if !postCompletion(j.Target) {
			return 0
		}
		fmt.Println()
	}
}

// postCompletion shows the after-copy menu. Open and reveal loop back to it; it
// returns true to copy another folder, false to quit.
func postCompletion(target string) bool {
	for {
		var action string
		err := wizard.Menu(huh.NewSelect[string]().
			Title("What next?").
			Options(
				huh.NewOption("Open the folder", "open"),
				huh.NewOption(revealLabel(), "reveal"),
				huh.NewOption("Do another directory", "another"),
				huh.NewOption("Quit", "quit"),
			).
			Value(&action), false)
		if err != nil {
			return false // Ctrl+C at the menu = quit
		}
		switch action {
		case "open":
			openInFileManager(target, false)
		case "reveal":
			openInFileManager(target, true)
		case "another":
			return true
		case "quit":
			return false
		}
	}
}

func revealLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return "Reveal in Finder"
	case "windows":
		return "Show in Explorer"
	default:
		return "Show in file manager"
	}
}

// openInFileManager opens target in the OS file manager. When reveal is true it
// selects the folder inside its parent (macOS/Windows) rather than opening it.
func openInFileManager(target string, reveal bool) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		if reveal {
			cmd = exec.Command("open", "-R", target)
		} else {
			cmd = exec.Command("open", target)
		}
	case "windows":
		if reveal {
			cmd = exec.Command("explorer", "/select,", target)
		} else {
			cmd = exec.Command("explorer", target)
		}
	default: // linux, *bsd
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "  "+ui.Red.Render("Could not open: ")+err.Error())
		return
	}
	_ = cmd.Process.Release()
}

// sendNotification says a finished run is done, when the job asked for that.
func sendNotification(j job.Job, sum engine.Summary) {
	if !j.Notify {
		return
	}
	body := fmt.Sprintf("%d files into %s", sum.Result.Copied+sum.Result.Cloned+sum.Result.Existing, filepath.Base(j.Target))
	if n := len(sum.Result.Failures); n > 0 {
		body += fmt.Sprintf("; %d failed", n)
	}
	title := "SuperDirectory finished"
	if sum.Result.Full {
		title = "SuperDirectory stopped: the destination is full"
	}
	_ = notify.Send(title, body) // a missed notification is not a failure
}

// ── the command line ───────────────────────────────────────────────────────

// list is a flag that may be repeated, and also takes comma-separated values.
type list []string

func (l *list) String() string { return strings.Join(*l, ",") }
func (l *list) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

// repeat is a flag that may be repeated, each value kept whole: paths may hold
// commas.
type repeat []string

func (r *repeat) String() string     { return strings.Join(*r, ",") }
func (r *repeat) Set(v string) error { *r = append(*r, v); return nil }

// copyOptions are the copy command's flags beyond the job itself.
type copyOptions struct {
	preset, savePreset string
	yes                bool
}

// parseCopy turns copy's flags into a job: a preset first when one is named, then
// every flag that was set on top of it.
func parseCopy(args []string, errOut io.Writer) (job.Job, copyOptions, error) {
	fs := flag.NewFlagSet("copy", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() { fmt.Fprint(errOut, usage) }

	var from, exclude, skip repeat
	var only, not, dups list
	var opt copyOptions
	var flags job.Job
	var layout, minSize, maxSize, to string
	fs.Var(&from, "from", "")
	fs.StringVar(&to, "to", "", "")
	fs.StringVar(&layout, "layout", "", "")
	fs.IntVar(&flags.Depth, "depth", 0, "")
	fs.BoolVar(&flags.KeepFolders, "keep-folders", false, "")
	fs.Var(&exclude, "exclude", "")
	fs.Var(&skip, "skip", "")
	fs.Var(&only, "only", "")
	fs.Var(&not, "not", "")
	fs.StringVar(&minSize, "min-size", "", "")
	fs.StringVar(&maxSize, "max-size", "", "")
	fs.StringVar(&flags.Since, "since", "", "")
	fs.StringVar(&flags.Until, "until", "", "")
	fs.Var(&dups, "duplicates", "")
	fs.BoolVar(&flags.Review, "review", false, "")
	fs.BoolVar(&flags.RenameTitles, "rename-titles", false, "")
	fs.BoolVar(&flags.DetectTypes, "detect-types", false, "")
	fs.BoolVar(&flags.Expand, "expand", false, "")
	fs.BoolVar(&flags.MergeText, "merge-text", false, "")
	fs.IntVar(&flags.Batch, "batch", 0, "")
	fs.BoolVar(&flags.Verify, "verify", false, "")
	fs.IntVar(&flags.Retries, "retries", 1, "")
	fs.BoolVar(&flags.NoReport, "no-report", false, "")
	fs.BoolVar(&flags.Notify, "notify", false, "")
	fs.StringVar(&opt.preset, "preset", "", "")
	fs.StringVar(&opt.savePreset, "save-preset", "", "")
	fs.BoolVar(&opt.yes, "yes", false, "")
	if err := fs.Parse(args); err != nil {
		return job.Job{}, opt, err
	}
	if fs.NArg() > 0 {
		return job.Job{}, opt, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	j := job.Job{Retries: 1}
	if opt.preset != "" {
		p, err := job.LoadPreset(opt.preset)
		if err != nil {
			return j, opt, err
		}
		j = p
	}
	// Without a preset every flag applies, defaults included; with one, only the
	// flags actually given override it.
	given := func(name string) bool { return set[name] || opt.preset == "" }

	if given("from") {
		j.Sources = nil
		for _, f := range from {
			j.Sources = append(j.Sources, absolute(f))
		}
	}
	if given("to") && to != "" {
		j.Target = absolute(to)
	}
	if given("layout") {
		j.Layout = job.Layout(layout)
	}
	if given("depth") {
		j.Depth = flags.Depth
	}
	if given("keep-folders") {
		j.KeepFolders = flags.KeepFolders
	}
	if given("exclude") {
		j.Excluded = nil
		for _, e := range exclude {
			j.Excluded = append(j.Excluded, absolute(e))
		}
	}
	if given("skip") {
		j.Skip = append([]string(nil), skip...)
	}
	if given("only") {
		j.Only = append([]string(nil), only...)
	}
	if given("not") {
		j.Not = append([]string(nil), not...)
	}
	var err error
	if given("min-size") {
		if j.MinSize, err = job.ParseSize(minSize); err != nil {
			return j, opt, fmt.Errorf("--min-size: %w", err)
		}
	}
	if given("max-size") {
		if j.MaxSize, err = job.ParseSize(maxSize); err != nil {
			return j, opt, fmt.Errorf("--max-size: %w", err)
		}
	}
	if given("since") {
		j.Since = flags.Since
	}
	if given("until") {
		j.Until = flags.Until
	}
	if given("duplicates") {
		j.Duplicates = nil
		for _, d := range dups {
			switch d {
			case "off", "none":
			case "all":
				j.Duplicates = []string{job.Identical, job.Pictures, job.Documents}
			default:
				j.Duplicates = append(j.Duplicates, d)
			}
		}
	}
	if given("review") {
		j.Review = flags.Review
	}
	if given("rename-titles") {
		j.RenameTitles = flags.RenameTitles
	}
	if given("detect-types") {
		j.DetectTypes = flags.DetectTypes
	}
	if given("expand") {
		j.Expand = flags.Expand
	}
	if given("merge-text") {
		j.MergeText = flags.MergeText
	}
	if given("batch") {
		j.Batch = flags.Batch
	}
	if given("verify") {
		j.Verify = flags.Verify
	}
	if given("retries") {
		j.Retries = flags.Retries
	}
	if given("no-report") {
		j.NoReport = flags.NoReport
	}
	if given("notify") {
		j.Notify = flags.Notify
	}
	return j, opt, nil
}

func absolute(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func copyCommand(args []string, errOut io.Writer) int {
	j, opt, err := parseCopy(args, errOut)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(errOut, "superdirectory:", err)
		}
		return 2
	}
	if err := j.Validate(); err != nil {
		fmt.Fprintln(errOut, "superdirectory:", err)
		return 2
	}
	if !engine.Interrupted(j.Target) {
		if entries, err := os.ReadDir(j.Target); err == nil && len(entries) > 0 {
			fmt.Fprintf(errOut, "superdirectory: %s already exists and is not empty; choose another --to\n", j.Target)
			return 2
		}
	}
	if opt.savePreset != "" {
		if err := job.SavePreset(opt.savePreset, j); err != nil {
			fmt.Fprintln(errOut, "superdirectory:", err)
			return 1
		}
		fmt.Fprintf(errOut, "superdirectory: saved preset %q\n", opt.savePreset)
	}
	if !opt.yes {
		if !term.IsTerminal(os.Stdin.Fd()) {
			fmt.Fprintln(errOut, "superdirectory: no terminal to confirm on; pass --yes to copy")
			return 2
		}
		fmt.Fprintln(errOut, indent(wizard.Summary(j)))
		fmt.Fprint(errOut, "Copy? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(errOut, "superdirectory: not copying")
			return 0
		}
	}
	return runPlain(j, opt.yes, errOut)
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

func resumeCommand(args []string, errOut io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(errOut, "superdirectory: resume takes one argument, the superdirectory to finish")
		return 2
	}
	target := absolute(args[0])
	state, err := engine.LoadState(target)
	if err != nil {
		fmt.Fprintf(errOut, "superdirectory: %s: %v\n", target, err)
		return 1
	}
	if state.Complete {
		fmt.Fprintf(errOut, "superdirectory: the copy into %s finished on %s; running it again copies only what changed\n",
			target, state.Finished.Format("2006-01-02 15:04"))
	}
	return runPlain(state.Job, true, errOut)
}

// runPlain runs a job with the command line's front end and returns the exit code.
func runPlain(j job.Job, yes bool, errOut io.Writer) int {
	stop := ui.NewStopper()
	release := stop.Catch()
	defer release()
	hooks := &ui.Plain{Out: errOut, Stop: stop, Yes: yes}
	if j.Review && interactive() {
		hooks.Review = reviewHook()
	}
	sum, err := engine.Run(j, hooks, stop.C())
	switch {
	case errors.Is(err, engine.ErrStopped):
		ui.PrintSummary(errOut, sum)
		return 130
	case errors.Is(err, engine.ErrAbandoned):
		return 1
	case err != nil:
		fmt.Fprintln(errOut, "superdirectory:", err)
		return 1
	}
	ui.PrintSummary(errOut, sum)
	sendNotification(j, sum)
	if len(sum.Result.Failures) > 0 {
		return 1
	}
	return 0
}

func presetsCommand(args []string, out, errOut io.Writer) int {
	if len(args) == 2 && args[0] == "delete" {
		if err := job.DeletePreset(args[1]); err != nil {
			fmt.Fprintln(errOut, "superdirectory:", err)
			return 1
		}
		return 0
	}
	if len(args) != 0 {
		fmt.Fprintln(errOut, "superdirectory: presets takes no arguments, or delete NAME")
		return 2
	}
	names, err := job.Presets()
	if err != nil {
		fmt.Fprintln(errOut, "superdirectory:", err)
		return 1
	}
	if len(names) == 0 {
		fmt.Fprintln(out, "No presets. Save one from the wizard's last screen, or with copy --save-preset NAME.")
		return 0
	}
	for _, n := range names {
		j, err := job.LoadPreset(n)
		if err != nil {
			fmt.Fprintf(out, "%s\t(damaged: %v)\n", n, err)
			continue
		}
		fmt.Fprintf(out, "%s\t%s layout, %d source(s)\n", n, j.LayoutOrFlat(), len(j.Sources))
	}
	return 0
}

func categoriesCommand(args []string, out, errOut io.Writer) int {
	dir, err := job.ConfigDir()
	if err != nil {
		fmt.Fprintln(errOut, "superdirectory:", err)
		return 1
	}
	path := filepath.Join(dir, "categories.json")
	table, err := organize.LoadTable(path)
	if err != nil {
		fmt.Fprintln(errOut, "superdirectory:", err)
		return 1
	}
	switch {
	case len(args) == 1 && args[0] == "--write":
		if _, err := os.Stat(path); err == nil {
			fmt.Fprintf(errOut, "superdirectory: %s already exists; edit it there\n", path)
			return 1
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(errOut, "superdirectory:", err)
			return 1
		}
		if err := job.WriteFileAtomic(path, table.JSON()); err != nil {
			fmt.Fprintln(errOut, "superdirectory:", err)
			return 1
		}
		fmt.Fprintf(out, "Wrote %s. Edit it; SuperDirectory reads it on every run.\n", path)
		return 0
	case len(args) != 0:
		fmt.Fprintln(errOut, "superdirectory: categories takes no arguments, or --write")
		return 2
	}
	fmt.Fprint(out, string(table.JSON()))
	fmt.Fprintf(out, "\nAny other extension goes to %s/. Table file: %s\n", organize.CategoryOther, path)
	return 0
}

// inspectCommand shows what SuperDirectory reads from inside each file in a
// folder: its detected type and its title.
func inspectCommand(args []string, out, errOut io.Writer) int {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintln(errOut, "superdirectory:", err)
		return 1
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].Name() < entries[b].Name() })
	ex := extract.MetadataExtractor{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m, err := ex.Extract(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		fmt.Fprintf(out, "%s\n    %s · %d bytes\n", m.Title, m.MIMEType, m.Size)
	}
	return 0
}

// reviewHook returns the duplicate review screen.
func reviewHook() func(f *engine.Found, sets []engine.DupSet) ([]engine.DupSet, error) {
	return ui.Review
}
