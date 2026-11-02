// Package notify zeigt macOS-Benachrichtigungen per osascript. Best-Effort:
// ein fehlgeschlagener Banner bricht den Lauf nicht ab.
package notify

import (
	"os/exec"
	"runtime"
	"strings"
)

// Notifier zeigt eine Benachrichtigung mit Titel und Text.
type Notifier interface {
	Notify(title, msg string)
}

// osascript ist der Notifier für macOS. Auf anderen Systemen tut er nichts.
// run führt das Programm aus; Produktion ist exec.Command(...).Run, Tests setzen einen Fake.
type osascript struct {
	goos string
	run  func(name string, args ...string) error
}

// New liefert den Notifier für das laufende System.
func New() Notifier {
	return &osascript{goos: runtime.GOOS, run: runCommand}
}

func runCommand(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// Notify zeigt die Benachrichtigung über osascript (nur auf macOS).
func (o *osascript) Notify(title, msg string) {
	if o.goos != "darwin" {
		return
	}
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	_ = o.run("osascript", "-e", "display notification "+q(msg)+" with title "+q(title)) //nolint:errcheck // Benachrichtigung ist Best-Effort, Fehler egal
}
