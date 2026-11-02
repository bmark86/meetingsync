// Package notify zeigt macOS-Benachrichtigungen per osascript. Best-Effort:
// ein fehlgeschlagener Banner bricht den Lauf nicht ab.
package notify

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// osascriptPath ist der feste Pfad von osascript (kein PATH-Lookup, Befund S-10).
const osascriptPath = "/usr/bin/osascript"

// Notifier zeigt eine Benachrichtigung mit Titel und Text.
type Notifier interface {
	Notify(title, msg string)
}

// osascript ist der Notifier für macOS. Auf anderen Systemen tut er nichts.
// run führt das Programm mit genau der Umgebung env aus; Produktion ist
// runCommand, Tests setzen einen Fake.
type osascript struct {
	goos string
	run  func(path string, env []string, args ...string) error
}

// New liefert den Notifier für das laufende System.
func New() Notifier {
	return &osascript{goos: runtime.GOOS, run: runCommand}
}

func runCommand(path string, env []string, args ...string) error {
	cmd := exec.Command(path, args...) //nolint:gosec // G204: einziger Aufrufer ist Notify mit der Konstante osascriptPath; Argumente gehen nicht durch eine Shell, Text ist für AppleScript escaped
	cmd.Env = env
	return cmd.Run()
}

// minimalEnv liefert die Umgebung für osascript: HOME, USER und LANG aus dem
// eigenen Prozess (nur wenn gesetzt) und ein fester PATH. Alles andere, etwa
// NOTION_TOKEN, wird nicht weitergegeben.
func minimalEnv() []string {
	var env []string
	for _, k := range []string{"HOME", "USER", "LANG"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, "PATH=/usr/bin:/bin")
}

// Notify zeigt die Benachrichtigung über osascript (nur auf macOS).
func (o *osascript) Notify(title, msg string) {
	if o.goos != "darwin" {
		return
	}
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	_ = o.run(osascriptPath, minimalEnv(), "-e", "display notification "+q(msg)+" with title "+q(title)) //nolint:errcheck // Benachrichtigung ist Best-Effort, Fehler egal
}
