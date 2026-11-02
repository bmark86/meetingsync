package main

// Pass-Ebene (R-M2-06): Benachrichtigung nur bei cfg.Notify, über den Notifier.
// Alle Tests nutzen einen Fake, nie das echte osascript.

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// fakeNotifier zeichnet Aufrufe als {Titel, Text} auf.
type fakeNotifier struct {
	mu    sync.Mutex
	calls [][2]string
}

func (f *fakeNotifier) Notify(title, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [2]string{title, msg})
}

func runNotifyPass(t *testing.T, notifyOn bool) [][2]string {
	t.Helper()
	f := &fakeServer{t: t, appends: map[string]int{}}
	app, dir := setup(t, f)
	app.cfg.Notify = notifyOn
	fn := &fakeNotifier{}
	app.notifier = fn
	writeOld(t, filepath.Join(dir, "Meeting 2026-10-28 14-00.txt"), srtLikeText(), 5*time.Minute)
	app.Pass(context.Background(), time.Now())
	if len(f.pages) != 2 {
		t.Fatalf("erwartet Notiz + 1 To-do, bekommen %d Seiten", len(f.pages))
	}
	return fn.calls
}

func TestPassNotifyOnOwnTodoSendsOneCall(t *testing.T) {
	got := runNotifyPass(t, true)
	want := [][2]string{{"Neue Meeting-Notiz", "Sirius Sync · 1 To-do(s) für dich"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Aufrufe = %q, want %q", got, want)
	}
}

func TestPassNotifyOffSendsNothing(t *testing.T) {
	if got := runNotifyPass(t, false); len(got) != 0 {
		t.Errorf("Aufrufe = %q, want keine", got)
	}
}
