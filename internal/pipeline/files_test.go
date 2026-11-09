package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meetingsync/internal/ledger"
	"meetingsync/internal/watch"
)

// Aus app_move_test.go, app_move_qa_test.go und app_toctou_test.go (Paket
// main) hierher verschoben: sie rufen fail, handle und archive direkt.

// moveEnv: Quelle src mit Inhalt "neu" und leerer Zielordner dir.
func moveEnv(t *testing.T) (src, dir string) {
	t.Helper()
	root := t.TempDir()
	src = filepath.Join(root, "a.txt")
	if err := os.WriteFile(src, []byte("neu"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src, filepath.Join(root, "ziel")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Fängt: vorhandene .fehler.txt wird überschrieben (os.WriteFile).
func TestFailFehlerTxtNoOverwrite(t *testing.T) {
	e := newEnv(t, nil)
	src, _ := moveEnv(t)
	old := filepath.Join(e.o.FailedDir, "a.txt.fehler.txt")
	writeFile(t, old, "alt")
	if !e.p.fail(errors.New("Ursache neu"), &watch.Entry{Path: src, Name: "a.txt"}) {
		t.Fatal("fail = false")
	}
	if got := mustRead(t, old); got != "alt" {
		t.Fatalf("vorhandener Hinweis überschrieben: %q", got)
	}
	if got := mustRead(t, filepath.Join(e.o.FailedDir, "a.txt (2).fehler.txt")); !strings.Contains(got, "Ursache neu") {
		t.Fatalf("neuer Hinweis = %q", got)
	}
}

// Name so lang, dass "<name>.fehler.txt" über NAME_MAX (255) liegt.
// Fängt: Schreibfehler der .fehler.txt wird ignoriert.
func TestFailFehlerTxtWriteErrorLogged(t *testing.T) {
	e := newEnv(t, nil)
	name := strings.Repeat("x", 250) + ".txt"
	src := filepath.Join(t.TempDir(), name)
	writeFile(t, src, "neu")
	if !e.p.fail(errors.New("Ursache"), &watch.Entry{Path: src, Name: name}) {
		t.Fatal("fail = false")
	}
	if !exists(filepath.Join(e.o.FailedDir, name)) {
		t.Fatal("Datei nicht verschoben")
	}
	if !strings.Contains(e.log.String(), "Fehlerhinweis zu "+name+" nicht geschrieben") {
		t.Fatalf("Schreibfehler nicht geloggt:\n%s", e.log.String())
	}
}

// A3 über fail(): .fehler.txt wird bei mehrfachem Fehlschlag derselben
// Namen nie überschrieben (a.txt.fehler.txt, a (2).txt.fehler.txt gehören zu
// verschiedenen Zielnamen, vorhandener Hinweis bleibt).
func TestQAFehlerTxtParallelCreatedNotOverwritten(t *testing.T) {
	e := newEnv(t, nil)
	src, _ := moveEnv(t)
	hint := filepath.Join(e.o.FailedDir, "a.txt.fehler.txt")
	writeFile(t, hint, "alt")
	writeFile(t, filepath.Join(e.o.FailedDir, "a.txt (2).fehler.txt"), "alt2")
	if !e.p.fail(errors.New("neu"), &watch.Entry{Path: src, Name: "a.txt"}) {
		t.Fatal("fail = false")
	}
	if mustRead(t, hint) != "alt" || mustRead(t, filepath.Join(e.o.FailedDir, "a.txt (2).fehler.txt")) != "alt2" {
		t.Fatal("Hinweis überschrieben")
	}
	if !strings.Contains(mustRead(t, filepath.Join(e.o.FailedDir, "a.txt (3).fehler.txt")), "neu") {
		t.Fatal("neuer Hinweis fehlt")
	}
}

const toctouWarn = "seit dem Einlesen geändert → nicht archiviert"

// rewrite schreibt content nach path und setzt die mtime auf ts.
func rewrite(t *testing.T, path, content string, ts time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // G703: Pfad unter t.TempDir()
		t.Error(err)
		return
	}
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Error(err)
	}
}

// Weg "schon verarbeitet" (Has): Hash im Ledger, Datei danach geändert →
// bleibt liegen mit Warnung. Fängt: Recheck nur auf dem Verarbeitungsweg.
func TestTOCTOUAlreadyProcessedChanged(t *testing.T) {
	e := newEnv(t, nil)
	p := e.write(t, "a.txt", "alt\n", testM)
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	stale := &watch.Entry{Path: p, Name: "a.txt", ModTime: st.ModTime(), Size: st.Size(), Content: []byte("alt\n")}
	hash := sha256Hex(stale.Content)
	if err := e.ledger.Add(ledger.Entry{Hash: hash, File: "a.txt", ModTime: st.ModTime(), At: testM}); err != nil {
		t.Fatal(err)
	}
	rewrite(t, p, "neu, länger\n", st.ModTime().Add(time.Second))

	e.p.handle(t.Context(), job{primary: stale}, testM)

	if !exists(p) || exists(e.archived("a.txt")) {
		t.Fatal("geänderte Datei archiviert")
	}
	if !strings.Contains(e.log.String(), "a.txt wurde "+toctouWarn) {
		t.Fatalf("Warnung fehlt:\n%s", e.log)
	}
}

// archive selbst prüft nicht (HasNear, orphan_summary_action=archive): auch ein
// veralteter Eintrag geht ins Archiv wie bisher. Fängt: Recheck in archive.
func TestTOCTOUArchiveNoRecheck(t *testing.T) {
	e := newEnv(t, nil)
	p := e.write(t, "a.txt", "alt\n", testM)
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	stale := &watch.Entry{Path: p, Name: "a.txt", ModTime: st.ModTime(), Size: st.Size()}
	rewrite(t, p, "neu, länger\n", st.ModTime().Add(time.Second))

	e.p.archive(stale)
	if !exists(e.archived("a.txt")) {
		t.Fatal("a.txt nicht im Archiv")
	}
	if strings.Contains(e.log.String(), toctouWarn) {
		t.Fatalf("Warnung in archive:\n%s", e.log)
	}
}
