package ledger

// Tests des Ledgers (R-M0-08, R-M1-01, R-M1-03): Laden, Anhängen, Statusmodell,
// Plattenformat und Kompatibilität mit v1.0.0 in beide Richtungen.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// flowM ist die mtime der Eingaben (UTC).
var flowM = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: Pfad unter t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// --- Ledger --------------------------------------------------------------------

// Einordnung nach R-M1-01 (Ledger mit Status, ADR 0004):
//   - Angepasst: mtimes gibt es nicht mehr, HasNear liest byHash
//     (TestLoadSkipsBrokenLines prüft das über HasNear);
//     TestLoadLongLine schreibt die Zeile im Plattenformat ledgerLine.
//   - Unverändert gültig: Limit 1 MB (TestLoadLineOverLimitFails),
//     kaputte Zeilen überspringen, Neustart-Reload (TestLoadReloadsAfterRestart,
//     TestPassAfterRestartUsesLoadedLedger), persist=false
//     (TestLedgerAddWithoutPersistKeepsFileUntouched).
//   - Neu: Status-Modell, Plattenformat, Kompatibilität mit v1.0.0 in beide
//     Richtungen, Sync-Fehler (Block "Ledger-Status" unten).

// Fängt: Add schreibt nicht / Load füllt byHash nicht.
func TestLoadLedgerReloadsAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", ".ledger.jsonl")
	l1, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Entry{
		{Hash: "h1", File: "a.srt", ModTime: flowM, URL: "u1", At: flowM},
		{Hash: "h2", File: "b.srt", ModTime: flowM.Add(time.Hour), URL: "u2", At: flowM},
	} {
		if err := l1.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	l2, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !l2.Has("h1") || !l2.Has("h2") || l2.Has("h3") {
		t.Fatalf("Has nach Neustart falsch: h1=%v h2=%v h3=%v", l2.Has("h1"), l2.Has("h2"), l2.Has("h3"))
	}
	if got := l2.byHash["h2"]; got.Status != StatusFertig || got.File != "b.srt" || got.URL != "u2" || !got.ModTime.Equal(flowM.Add(time.Hour)) {
		t.Fatalf("Eintrag h2 nicht vollständig geladen: %+v", got)
	}
	w := 3 * time.Minute
	if !l2.HasNear(flowM.Add(w), w) || l2.HasNear(flowM.Add(w+time.Second), w) {
		t.Fatal("HasNear muss nach Neustart mit Fenstergrenze (<=) arbeiten")
	}
	// weiterer Eintrag nach dem Neustart hängt an, überschreibt nicht
	if err := l2.Add(Entry{Hash: "h3", ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	l3, _ := Load(path, true)
	if !l3.Has("h1") || !l3.Has("h3") {
		t.Fatal("Add nach Neustart darf frühere Zeilen nicht verlieren")
	}
}

// Fängt: kaputte Zeilen brechen das Laden ab / Einträge ohne hash werden übernommen.
func TestLoadLedgerSkipsBrokenLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	lines := []string{
		`{"hash":"ok1","file":"a","mtime":"2026-09-30T12:00:00Z"}`,
		`das ist kein json`,
		``,
		`{"file":"ohne-hash","mtime":"2026-09-30T13:00:00Z"}`,
		`{"hash":"","file":"leerer-hash"}`,
		`{"hash":"abgeschnitten","fi`,
		`{"hash":"ok2","file":"b","mtime":"2026-09-30T14:00:00Z"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Load(path, false)
	if err != nil {
		t.Fatalf("kaputte Zeilen dürfen keinen Fehler liefern: %v", err)
	}
	if len(l.byHash) != 2 || !l.Has("ok1") || !l.Has("ok2") {
		t.Fatalf("nur ok1 und ok2 erwartet, got %v", l.byHash)
	}
	if l.HasNear(flowM.Add(time.Hour), 59*time.Minute) {
		t.Fatal("HasNear: Einträge ohne hash zählen nicht")
	}
}

// Fängt: Scanner-Puffer bleibt bei 64 KB (bufio-Standard) statt 1 MB.
func TestLoadLedgerLongLine(t *testing.T) {
	dir := t.TempDir()
	long, _ := json.Marshal(ledgerLine{Hash: "lang", File: strings.Repeat("x", 100*1024), ModTime: flowM})
	if len(long) <= 64*1024 {
		t.Fatal("Testzeile muss länger als 64 KB sein")
	}
	path := filepath.Join(dir, "l.jsonl")
	data := `{"hash":"vorher"}` + "\n" + string(long) + "\n" + `{"hash":"nachher"}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"vorher", "lang", "nachher"} {
		if !l.Has(h) {
			t.Fatalf("Hash %q fehlt", h)
		}
	}
}

// Charakterisierung: Zeile über 1 MB bricht das Laden mit bufio.ErrTooLong ab.
// Fängt: stilles Weiterlesen oder Panic bei zu langen Zeilen.
func TestLoadLedgerLineOverLimitFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	data := `{"hash":"vorher"}` + "\n" + strings.Repeat("x", (1<<20)+10) + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Load(path, false)
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("err = %v, want bufio.ErrTooLong", err)
	}
	if l == nil || !l.Has("vorher") {
		t.Fatal("bis dahin gelesene Einträge bleiben erhalten")
	}
}

func TestLoadLedgerMissingFileIsEmpty(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "gibt-es-nicht.jsonl"), true)
	if err != nil || l == nil || len(l.byHash) != 0 || l.HasNear(flowM, time.Hour) {
		t.Fatalf("leeres Ledger erwartet, got l=%v err=%v", l, err)
	}
}

// Fängt: Fehler außer "nicht vorhanden" werden verschluckt.
func TestLoadLedgerOpenErrorIsReturned(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "datei")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Load(filepath.Join(blocker, "l.jsonl"), true) // ENOTDIR
	if err == nil || l != nil {
		t.Fatalf("Fehler erwartet, got l=%v err=%v", l, err)
	}
}

// Fängt: persist=false schreibt trotzdem (Dry-Run würde das Protokoll füllen).
func TestLedgerAddWithoutPersistKeepsFileUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x", "l.jsonl")
	l, _ := Load(path, false)
	if err := l.Add(Entry{Hash: "h", ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	if !l.Has("h") {
		t.Fatal("im Speicher muss der Eintrag stehen")
	}
	if exists(path) || exists(filepath.Dir(path)) {
		t.Fatal("persist=false darf nichts auf die Platte schreiben")
	}
}

// Fängt: Fehler von MkdirAll/OpenFile werden verschluckt. Der Pfad liegt unter
// einer Datei (ENOTDIR), Write wird gar nicht erreicht; Write/Sync-Fehler
// prüft TestLedgerAddReturnsSyncError.
func TestLedgerAddReturnsOpenError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "datei")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &Ledger{path: filepath.Join(blocker, "l.jsonl"), persist: true, byHash: map[string]Entry{}}
	if err := l.Add(Entry{Hash: "h"}); err == nil {
		t.Fatal("Fehler beim Öffnen muss zurückgegeben werden")
	}
}

// Fängt: die neue Zeile wird an eine abgerissene letzte Zeile angehängt
// (verschmilzt, geht beim Laden verloren) / der Normalpfad bekommt ein
// zusätzliches '\n'.
func TestLedgerAddAfterTornLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	torn := `{"hash":"alt","file":"a"}` + "\n" + `{"sha256":"abgerissen","sta`
	if err := os.WriteFile(path, []byte(torn), 0o644); err != nil {
		t.Fatal(err)
	}
	l, _ := Load(path, true)
	for _, h := range []string{"neu1", "neu2"} {
		if err := l.Add(Entry{Hash: h, ModTime: flowM, At: flowM}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(path) //nolint:gosec // G304: Pfad unter t.TempDir()
	line := func(h string) string {
		return `{"hash":"` + h + `","file":"","mtime":"2026-09-30T12:00:00Z","url":"","at":"2026-09-30T12:00:00Z"}` + "\n"
	}
	if want := torn + "\n" + line("neu1") + line("neu2"); string(got) != want {
		t.Fatalf("Ledger-Datei:\n%q\nwant:\n%q", got, want)
	}
	re, err := Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !re.Has("alt") || !re.Has("neu1") || !re.Has("neu2") || re.Started("abgerissen") {
		t.Fatalf("nach Neuladen: %v", re.byHash)
	}
}

// Fängt: kein Ordner-Sync beim ersten Anlegen (ADR 0004) / Ordner-Sync bei
// jedem Add. Der Ordner ist schreib-, aber nicht lesbar (0300): Datei anlegen
// geht, Ordner öffnen für Sync nicht.
func TestLedgerAddSyncsDirOnCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	if err := os.Mkdir(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) //nolint:gosec // G302: TempDir aufräumen
	path := filepath.Join(dir, "l.jsonl")
	l, _ := Load(path, true)
	if err := l.Add(Entry{Hash: "h1"}); err == nil {
		t.Fatal("erster Add: Fehler vom Ordner-Sync erwartet")
	}
	if err := l.Add(Entry{Hash: "h2"}); err != nil {
		t.Fatalf("Datei existiert: kein Ordner-Sync, kein Fehler erwartet: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // G302: TempDir
		t.Fatal(err)
	}
	re, _ := Load(path, false)
	if !re.Has("h1") || !re.Has("h2") {
		t.Fatal("beide Zeilen stehen trotz Sync-Fehler in der Datei")
	}
	if err := syncDir(dir); err != nil {
		t.Fatalf("Ordner-Sync auf macOS: %v", err)
	}
}

// Fängt: unbekannter Status zählt als fertig oder begonnen / sha256-Zeile
// ohne Status zählt nicht als fertig (ADR 0004).
func TestLoadLedgerUnknownStatusAndSHA256WithoutStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	data := `{"sha256":"neu","status":"pausiert","file":"a"}` + "\n" + `{"sha256":"ohne","file":"b"}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if l.Has("neu") || l.Started("neu") {
		t.Fatal("unbekannter Status: weder fertig noch begonnen (läuft neu)")
	}
	if !l.Has("ohne") {
		t.Fatal("sha256 ohne Status = fertig")
	}
}

// --- Ledger-Status (R-M1-01, ADR 0004) ---------------------------------------

// Fängt: Add ruft f.Sync nicht auf oder verschluckt dessen Fehler.
// /dev/null lässt Write zu, fsync schlägt dort fehl (macOS: ENOTSUP).
func TestLedgerAddReturnsSyncError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	if err := os.Symlink(os.DevNull, path); err != nil {
		t.Fatal(err)
	}
	l, _ := Load(filepath.Join(t.TempDir(), "leer.jsonl"), true)
	l.path = path
	if err := l.Add(Entry{Hash: "h", ModTime: flowM}); err == nil {
		t.Fatal("Sync-Fehler muss zurückgegeben werden")
	}
	if !l.Has("h") {
		t.Fatal("Eintrag bleibt trotz Schreibfehler im Speicher")
	}
}

// Fängt: Has/HasNear zählen begonnen oder verworfen / nicht die letzte Zeile gewinnt.
func TestLedgerStatusLastLineWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	l, _ := Load(path, true)
	w := time.Minute
	steps := []struct {
		status string
		want   bool
	}{
		{StatusBegonnen, false},
		{StatusFertig, true},
		{StatusVerworfen, false},
		{"", true}, // leerer Status = fertig
		{StatusBegonnen, false},
	}
	for i, s := range steps {
		if err := l.Add(Entry{Hash: "h", Status: s.status, ModTime: flowM}); err != nil {
			t.Fatal(err)
		}
		if l.Has("h") != s.want || l.HasNear(flowM, w) != s.want {
			t.Fatalf("Schritt %d (%q): Has=%v HasNear=%v, want %v", i, s.status, l.Has("h"), l.HasNear(flowM, w), s.want)
		}
		re, err := Load(path, false)
		if err != nil {
			t.Fatal(err)
		}
		if re.Has("h") != s.want || re.HasNear(flowM, w) != s.want {
			t.Fatalf("Schritt %d (%q) nach Neuladen: Has=%v HasNear=%v, want %v", i, s.status, re.Has("h"), re.HasNear(flowM, w), s.want)
		}
	}
	if got := l.byHash["h"].Status; got != StatusBegonnen {
		t.Fatalf("Status = %q, want begonnen", got)
	}
}

// Fängt: fertige Zeilen weichen vom v1.0.0-Format ab (Golden ledger.jsonl) /
// nicht fertige Zeilen tragen "hash" und würden von v1.0.0 gezählt.
func TestLedgerLineFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	l, _ := Load(path, true)
	at := flowM.Add(time.Minute)
	for _, st := range []string{"", StatusFertig, StatusBegonnen, StatusVerworfen} {
		if err := l.Add(Entry{Hash: "h", Status: st, File: "a.srt", ModTime: flowM, URL: "u", At: at}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: Pfad unter t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	fertig := `{"hash":"h","file":"a.srt","mtime":"2026-09-30T12:00:00Z","url":"u","at":"2026-09-30T12:01:00Z"}`
	want := strings.Join([]string{
		fertig,
		fertig,
		`{"sha256":"h","status":"begonnen","file":"a.srt","mtime":"2026-09-30T12:00:00Z","url":"u","at":"2026-09-30T12:01:00Z"}`,
		`{"sha256":"h","status":"verworfen","file":"a.srt","mtime":"2026-09-30T12:00:00Z","url":"u","at":"2026-09-30T12:01:00Z"}`,
	}, "\n") + "\n"
	if string(b) != want {
		t.Fatalf("Ledger-Datei:\n%s\nwant:\n%s", b, want)
	}
}

// Fängt: alte Zeilen ohne Status werden nicht als fertig gelesen (alt → neu).
func TestLoadLedgerV100File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	data, err := os.ReadFile(filepath.Join("testdata", "ledger-v1.0.0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // G703: Pfad unter t.TempDir(), Inhalt aus testdata
		t.Fatal(err)
	}
	l, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"1111", "2222", "3333"} {
		h = strings.Repeat(h, 16)
		if !l.Has(h) || l.byHash[h].Status != StatusFertig {
			t.Fatalf("v1.0.0-Zeile %s… nicht als fertig gelesen: %+v", h[:4], l.byHash[h])
		}
	}
	if !l.HasNear(time.Date(2026, 10, 1, 7, 31, 0, 0, time.UTC), time.Minute) {
		t.Fatal("HasNear muss v1.0.0-Zeilen zählen (mtime mit Zeitzonen-Offset)")
	}
	// neue Zeilen werden an die alte Datei angehängt, alte bleiben gültig
	if err := l.Add(Entry{Hash: strings.Repeat("1111", 16), Status: StatusVerworfen}); err != nil {
		t.Fatal(err)
	}
	re, _ := Load(path, false)
	if re.Has(strings.Repeat("1111", 16)) || !re.Has(strings.Repeat("2222", 16)) {
		t.Fatal("nach verworfen: 1111 nicht fertig, 2222 weiter fertig erwartet")
	}
	got, _ := os.ReadFile(path) //nolint:gosec // G304: Pfad unter t.TempDir()
	if !bytes.HasPrefix(got, data) {
		t.Fatal("die v1.0.0-Zeilen müssen unverändert bleiben")
	}
}

// v100Load bildet die Ladelogik von v1.0.0 nach (git show v1.0.0:app.go,
// Load und Entry). Nicht anpassen: sie steht für das alte Binary.
func v100Load(path string) (map[string]bool, []time.Time, error) {
	type v100Entry struct {
		Hash    string    `json:"hash"`
		File    string    `json:"file"`
		ModTime time.Time `json:"mtime"`
		URL     string    `json:"url"`
		At      time.Time `json:"at"`
	}
	byHash, mtimes := map[string]bool{}, []time.Time{}
	f, err := os.Open(path) //nolint:gosec // G304: Pfad unter t.TempDir()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e v100Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Hash != "" {
			byHash[e.Hash] = true
			mtimes = append(mtimes, e.ModTime)
		}
	}
	return byHash, mtimes, sc.Err()
}

// Fängt: v1.0.0 wertet begonnen/verworfen als verarbeitet (neu → alt) und
// archiviert die Datei dann ohne Notiz.
func TestV100LoaderIgnoresNonFinalLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	l, _ := Load(path, true)
	for _, e := range []Entry{
		{Hash: "begonnen", Status: StatusBegonnen, ModTime: flowM},
		{Hash: "verworfen", Status: StatusVerworfen, ModTime: flowM.Add(time.Hour)},
		{Hash: "fertig", Status: StatusFertig, ModTime: flowM.Add(2 * time.Hour)},
		{Hash: "spaeter", Status: StatusBegonnen, ModTime: flowM.Add(3 * time.Hour)},
		{Hash: "spaeter", Status: StatusFertig, ModTime: flowM.Add(3 * time.Hour)},
		{Hash: "alt", ModTime: flowM.Add(4 * time.Hour)},
	} {
		if err := l.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	byHash, mtimes, err := v100Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if byHash["begonnen"] || byHash["verworfen"] || !byHash["fertig"] || !byHash["spaeter"] || !byHash["alt"] || len(byHash) != 3 {
		t.Fatalf("v1.0.0 sieht %v, want nur fertig, spaeter, alt", byHash)
	}
	if len(mtimes) != 3 {
		t.Fatalf("v1.0.0 mtimes = %v, want 3 (nur fertige Zeilen, HasNear)", mtimes)
	}
	for _, m := range mtimes {
		if m.Equal(flowM) || m.Equal(flowM.Add(time.Hour)) {
			t.Fatalf("mtime %v einer nicht fertigen Zeile zählt in v1.0.0 für HasNear", m)
		}
	}
}

// Fängt (Mutation: omitempty an todos_fehlen/bloecke_fehler entfernt): normale
// fertig-Zeilen weichen von v1.0.0 ab. Und (Mutation: Lücken-Zeilen im
// sha256-Format): v1.0.0 sieht die Notiz nicht als verarbeitet.
func TestLedgerGapFieldsAndV100Compat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.jsonl")
	l, _ := Load(path, true)
	at := flowM.Add(time.Minute)
	for _, e := range []Entry{
		{Hash: "ok", File: "a.srt", ModTime: flowM, URL: "u", At: at},
		{Hash: "luecke", File: "b.srt", ModTime: flowM.Add(time.Hour), URL: "u", At: at, MissingTasks: 2, BlockErrors: 1},
	} {
		if err := l.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	want := `{"hash":"ok","file":"a.srt","mtime":"2026-09-30T12:00:00Z","url":"u","at":"2026-09-30T12:01:00Z"}` + "\n" +
		`{"hash":"luecke","file":"b.srt","mtime":"2026-09-30T13:00:00Z","url":"u","at":"2026-09-30T12:01:00Z","todos_fehlen":2,"bloecke_fehler":1}` + "\n"
	if got := mustRead(t, path); got != want {
		t.Fatalf("Ledger-Datei:\n%s\nwant:\n%s", got, want)
	}
	re, err := Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !re.Has("ok") || !re.Has("luecke") {
		t.Fatal("neu geladen: Lücken-Zeile zählt nicht als fertig")
	}
	byHash, mtimes, err := v100Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !byHash["ok"] || !byHash["luecke"] || len(mtimes) != 2 {
		t.Fatalf("v1.0.0 sieht %v / %v, want beide fertig", byHash, mtimes)
	}
}
