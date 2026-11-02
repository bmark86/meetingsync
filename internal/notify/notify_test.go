package notify

// Tests der Benachrichtigung (R-M2-06): Programm und Argumentliste per Fake-Runner,
// ohne echtes osascript.

import (
	"errors"
	"reflect"
	"testing"
)

// fakeRun zeichnet die Aufrufe auf und liefert den gesetzten Fehler.
type fakeRun struct {
	calls [][]string
	err   error
}

func (f *fakeRun) run(name string, args ...string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.err
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
			want: []string{"osascript", "-e",
				`display notification "Projektplanung · 2 To-do(s) für dich" with title "Neue Meeting-Notiz"`},
		},
		{
			name:  "Anführungszeichen und Backslashes werden escaped",
			title: `Titel "A" \B`,
			msg:   `Say "hi" \ there`,
			want: []string{"osascript", "-e",
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
