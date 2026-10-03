// Package notify shows a desktop notification, so a copy that runs for an hour can
// say it is done to someone who walked away from the terminal. It is opt-in: a
// notification nobody asked for is an interruption.
package notify

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// Send shows title and body through the platform's own notifier: osascript on
// macOS, notify-send on Linux and BSD, a PowerShell balloon on Windows. It returns
// an error when there is none; callers treat that as a missed nicety, not a failure.
func Send(title, body string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		script := "display notification " + appleString(body) + " with title " + appleString(title)
		cmd = exec.Command("osascript", "-e", script)
	case "windows":
		ps := "[void][Reflection.Assembly]::LoadWithPartialName('System.Windows.Forms');" +
			"$n=New-Object System.Windows.Forms.NotifyIcon;" +
			"$n.Icon=[System.Drawing.SystemIcons]::Information;$n.Visible=$true;" +
			"$n.ShowBalloonTip(8000," + psString(title) + "," + psString(body) + ",'Info');" +
			"Start-Sleep -Seconds 8;$n.Dispose()"
		cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	default:
		path, err := exec.LookPath("notify-send")
		if err != nil {
			return errors.New("no notifier: notify-send is not installed")
		}
		cmd = exec.Command(path, "--app-name=SuperDirectory", title, body)
	}
	if runtime.GOOS == "windows" {
		return cmd.Start() // the balloon outlives the command's own work; do not wait
	}
	return cmd.Run()
}

// appleString quotes s as an AppleScript string literal. The text comes from file
// names, so quotes and backslashes must not end the literal early.
func appleString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// psString quotes s as a single-quoted PowerShell literal, where the only escape
// is a doubled quote.
func psString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
