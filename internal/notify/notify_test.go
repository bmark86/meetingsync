package notify

// Tests der Benachrichtigung (R-M2-06): Programm und Argumentliste per Fake-Runner,
// ohne echtes osascript.

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

// fakeRun zeichnet die Aufrufe (Pfad + Argumente) und die Umgebungen auf und
// liefert den gesetzten Fehler.
type fakeRun struct {
	calls [][]string
	envs  [][]string
	err   error
}

func (f *fakeRun) run(path string, env []string, args ...string) error {
	f.calls = append(f.calls, append([]string{path}, args...))
	f.envs = append(f.envs, env)
	return f.err
}

// Fängt: osascript erbt die volle Umgebung (NOTION_TOKEN, fremder PATH) oder
// bekommt HOME/USER/LANG nicht.
func TestNotifyMinimalEnv(t *testing.T) {
	t.Setenv("NOTION_TOKEN", "ntn_geheim")
	t.Setenv("HOME", "/Users/test")
	t.Setenv("USER", "test")
	t.Setenv("LANG", "de_DE.UTF-8")
	t.Setenv("PATH", "/tmp/boese:/usr/bin")
	f := &fakeRun{}
	(&osascript{goos: "darwin", run: f.run}).Notify("T", "M")
	want := [][]string{{"HOME=/Users/test", "USER=test", "LANG=de_DE.UTF-8", "PATH=/usr/bin:/bin"}}
	if !reflect.DeepEqual(f.envs, want) {
		t.Fatalf("Umgebung = %q, want %q", f.envs, want)
	}
}

// Fängt: nicht gesetzte Variablen werden als leere Einträge mitgegeben.
func TestNotifyMinimalEnvOnlySetVars(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USER", "test")
	t.Setenv("LANG", "")
	for _, k := range []string{"HOME", "LANG"} {
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeRun{}
	(&osascript{goos: "darwin", run: f.run}).Notify("T", "M")
	want := [][]string{{"USER=test", "PATH=/usr/bin:/bin"}}
	if !reflect.DeepEqual(f.envs, want) {
		t.Fatalf("Umgebung = %q, want %q", f.envs, want)
	}
}

// Fängt: runCommand setzt die Umgebung nicht. Harmloses Programm, nie osascript.
func TestRunCommandUsesEnv(t *testing.T) {
	t.Setenv("MEETINGSYNC_ELTERN", "x")
	script := `[ "$A" = 1 ] && [ -z "$MEETINGSYNC_ELTERN" ]`
	if err := runCommand("/bin/sh", []string{"A=1", "PATH=/usr/bin:/bin"}, "-c", script); err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	if err := runCommand("/bin/sh", nil, "-c", "exit 3"); err == nil {
		t.Fatal("runCommand: Fehler erwartet")
	}
}

func TestNotifyArguments(t *testing.T) {
	tests := []struct {
		name  string
		title string
		msg   string
		want  []string
	}{
		{
			name:  "normal",
			title: "Neue Meeting-Notiz",
			msg:   "Projektplanung · 2 To-do(s) für dich",
			want: []string{"/usr/bin/osascript", "-e",
				`display notification "Projektplanung · 2 To-do(s) für dich" with title "Neue Meeting-Notiz"`},
		},
		{
			name:  "Anführungszeichen und Backslashes werden escaped",
			title: `Titel "A" \B`,
			msg:   `Say "hi" \ there`,
			want: []string{"/usr/bin/osascript", "-e",
				`display notification "Say \"hi\" \\ there" with title "Titel \"A\" \\B"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRun{}
			n := &osascript{goos: "darwin", run: f.run}
			n.Notify(tt.title, tt.msg)
			if len(f.calls) != 1 {
				t.Fatalf("Aufrufe = %d, want 1", len(f.calls))
			}
			if !reflect.DeepEqual(f.calls[0], tt.want) {
				t.Errorf("Aufruf = %#v\nwant %#v", f.calls[0], tt.want)
			}
		})
	}
}

func TestNotifyBestEffortIgnoresRunnerError(t *testing.T) {
	f := &fakeRun{err: errors.New("osascript fehlt")}
	n := &osascript{goos: "darwin", run: f.run}
	n.Notify("T", "M") // darf nicht panicken, Fehler wird verworfen
	if len(f.calls) != 1 {
		t.Fatalf("Aufrufe = %d, want 1", len(f.calls))
	}
}

func TestNotifyNoopOffMacOS(t *testing.T) {
	f := &fakeRun{}
	n := &osascript{goos: "linux", run: f.run}
	n.Notify("T", "M")
	if len(f.calls) != 0 {
		t.Errorf("Aufrufe = %v, want keine", f.calls)
	}
}

func TestNewReturnsNotifier(t *testing.T) {
	if New() == nil {
		t.Error("New() liefert nil")
	}
}
