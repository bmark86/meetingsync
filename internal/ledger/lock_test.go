package ledger

// Tests der Sperre und von Refresh (R-M3-14): zwei Prozesse (Dienst und -file)
// auf demselben Protokoll, hier zwei Ledger-Instanzen bzw. Dateideskriptoren.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Fängt: zweiter Halter bekommt die Sperre / Unlock gibt sie nicht frei /
// fehlender Ordner wird nicht gemeldet.
func TestTryLock(t *testing.T) {
	dir := t.TempDir()
	a, err := TryLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryLock(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("zweiter Halter: err = %v, want ErrLocked", err)
	}
	if err := a.Unlock(); err != nil {
		t.Fatal(err)
	}
	b, err := TryLock(dir)
	if err != nil {
		t.Fatalf("nach Unlock: %v", err)
	}
	_ = b.Unlock()
	if _, err := TryLock(filepath.Join(dir, "fehlt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fehlender Ordner: err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("Sperre legt Dateien an: %v", entries)
	}
}

// Fängt: WaitLock wartet nicht auf eine gehaltene Sperre / meldet das Warten
// nicht oder mehrfach / endet nicht mit ctx.
func TestWaitLock(t *testing.T) {
	dir := t.TempDir()
	held, err := TryLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	waits := 0
	got := make(chan error, 1)
	go func() {
		l, err := WaitLock(context.Background(), dir, 5*time.Millisecond, func() { waits++ })
		if err == nil {
			err = l.Unlock()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("WaitLock kam trotz gehaltener Sperre zurück: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := held.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := <-got; err != nil || waits != 1 {
		t.Fatalf("err = %v, Wartemeldungen %d, want nil, 1", err, waits)
	}

	held, _ = TryLock(dir)
	defer func() { _ = held.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := WaitLock(ctx, dir, 5*time.Millisecond, func() {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ctx: err = %v", err)
	}
}

// Fängt: Refresh sieht Zeilen einer anderen Instanz nicht / liest eigene
// Zeilen erneut (fertig im Speicher fiele nach einem Schreibfehler auf
// begonnen zurück) / reagiert nicht auf eine gekürzte Datei.
func TestRefreshSeesOtherInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	svc, _ := Load(path, true)
	other, _ := Load(path, true)
	if err := svc.Add(Entry{Hash: "eigen", Status: StatusBegonnen, ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	if err := other.Add(Entry{Hash: "fremd", ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	if svc.Has("fremd") {
		t.Fatal("ohne Refresh schon sichtbar?")
	}
	if err := svc.Refresh(); err != nil || !svc.Has("fremd") || !svc.Started("eigen") {
		t.Fatalf("Refresh: err=%v %v", err, svc.byHash)
	}
	// eigene Zeile nach dem Nachlesen: Refresh darf sie nicht noch einmal lesen
	if err := svc.Add(Entry{Hash: "eigen2", Status: StatusBegonnen, ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	// fertig scheitert beim Schreiben: im Speicher fertig, auf Platte begonnen
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := svc.Add(Entry{Hash: "eigen2", ModTime: flowM}); err == nil {
		t.Fatal("Schreibfehler erwartet")
	}
	if err := svc.Refresh(); err != nil || !svc.Has("eigen2") {
		t.Fatalf("eigene Zeile erneut gelesen: err=%v %v", err, svc.byHash["eigen2"])
	}
	// von Hand gekürzt: neu von vorn
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hash":"neu"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.Refresh(); err != nil || !svc.Has("neu") || svc.Has("fremd") {
		t.Fatalf("gekürzte Datei: err=%v %v", err, svc.byHash)
	}
}

// Fängt: eine abgerissene letzte Zeile verschiebt den Lesestand (die danach
// vervollständigte Zeile ginge verloren) / Refresh ohne Datei meldet Fehler.
func TestRefreshTornLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	svc, _ := Load(path, false)
	if err := svc.Refresh(); err != nil {
		t.Fatalf("ohne Datei: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"hash":"a"}`+"\n"+`{"hash":"b`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.Refresh(); err != nil || !svc.Has("a") || svc.Has("b") {
		t.Fatalf("err=%v %v", err, svc.byHash)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // G304: Pfad unter t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`"}` + "\n" + `{"hash":"c"}` + "\n")
	_ = f.Close()
	if err := svc.Refresh(); err != nil || !svc.Has("b") || !svc.Has("c") {
		t.Fatalf("nach Vervollständigen: err=%v %v", err, svc.byHash)
	}
}
