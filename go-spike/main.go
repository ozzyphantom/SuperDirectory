// SuperDirectory — Go spike.
//
// Proof-of-concept for the Go rewrite. Demonstrates three things the language
// decision hinges on:
//  1. The functional core (walk + collision-free copy) in package flatten.
//  2. A Charm/huh interactive wizard that replaces questionary+prompt_toolkit.
//  3. The content-inspection seam (package extract) where a future Python
//     backend can plug in without touching the Go core.
//
// Run the wizard:      go run .
// Try the extractor:   go run . inspect <dir>
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"superdirectory-spike/internal/extract"
	"superdirectory-spike/internal/flatten"
	"superdirectory-spike/internal/wizard"
)

var (
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("#2ecc71"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("#e74c3c"))
	orange = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	bold   = lipgloss.NewStyle().Bold(true)
	key    = lipgloss.NewStyle().Foreground(lipgloss.Color("#00b4d8")).Bold(true)
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "inspect" {
		runInspect(os.Args[2:])
		return
	}

	printIntro()
	for {
		target, completed := runOnce()
		if !completed {
			fmt.Println("\n  Exiting.")
			return
		}
		if !postCompletion(target) {
			return
		}
		fmt.Println()
	}
}

func printIntro() {
	fmt.Println()
	fmt.Println("  " + cyan.Render("SuperDirectory") + dim.Render("  ·  Go spike"))
	fmt.Println("  " + dim.Render("Flatten a nested tree into one folder.  ") + key.Render("Ctrl+C") + dim.Render(" exits anytime."))
}

// runOnce drives one full flatten. Returns the created target directory and
// whether it completed (false means the user aborted).
func runOnce() (string, bool) {
	res, err := wizard.Run()
	if err != nil {
		if wizard.IsAbort(err) {
			return "", false
		}
		fmt.Fprintln(os.Stderr, "\n  "+red.Render("Error: ")+err.Error())
		return "", false
	}

	items, err := flatten.Plan(res.Source, res.Excluded)
	if err != nil {
		fmt.Fprintln(os.Stderr, "  "+red.Render("Error building plan: ")+err.Error())
		return "", false
	}
	if err := os.MkdirAll(res.Target, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "  "+red.Render("Error creating target: ")+err.Error())
		return "", false
	}

	total := len(items)
	fmt.Println()
	if total == 0 {
		fmt.Println("  " + dim.Render("No files found to copy."))
		return res.Target, true
	}
	fmt.Printf("  Copying %s file(s) into %s\n\n", bold.Render(fmt.Sprintf("%d", total)), orange.Render(res.Target))

	failures := flatten.Copy(res.Target, items, renderProgress)
	if len(failures) > 0 {
		fmt.Printf("\n  %s %d file(s) could not be copied:\n\n", red.Render("⚠"), len(failures))
		for _, f := range failures {
			fmt.Printf("    %s  %s\n       %s\n", red.Render("✗"), f.Src, dim.Render(f.Err.Error()))
		}
	}
	fmt.Println("\n  " + green.Render(bold.Render("Finished!")))
	return res.Target, true
}

// postCompletion shows the after-copy menu. Open/reveal loop back to the menu;
// it returns true to flatten another directory, false to quit.
func postCompletion(target string) bool {
	for {
		var action string
		form := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("What next?").
				Options(
					huh.NewOption("Open the folder", "open"),
					huh.NewOption(revealLabel(), "reveal"),
					huh.NewOption("Flatten another directory", "another"),
					huh.NewOption("Quit", "quit"),
				).
				Value(&action),
		)).WithTheme(wizard.Theme())
		if err := form.Run(); err != nil {
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
		fmt.Fprintln(os.Stderr, "  "+red.Render("Could not open: ")+err.Error())
		return
	}
	_ = cmd.Process.Release()
}

func renderProgress(done, total int) {
	const width = 40
	filled := width * done / total
	bar := green.Render(strings.Repeat("█", filled)) + dim.Render(strings.Repeat("░", width-filled))
	fmt.Printf("\r  [%s] %3d%%  (%d/%d)", bar, done*100/total, done, total)
	if done == total {
		fmt.Println()
	}
}

// runInspect exercises the pure-Go content extractor over one directory,
// proving the seam works end to end with zero external dependencies.
func runInspect(args []string) {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "  "+red.Render("Error: ")+err.Error())
		os.Exit(1)
	}

	ex := extract.MetadataExtractor{}
	fmt.Println()
	fmt.Println("  " + bold.Render("Content inspection") + dim.Render("   pure-Go metadata extractor"))
	fmt.Println()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m, err := ex.Extract(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		fmt.Printf("  %s\n    %s · %s\n", cyan.Render(m.Title), dim.Render(m.MIMEType), dim.Render(fmt.Sprintf("%d bytes", m.Size)))
	}
	fmt.Println()
}
