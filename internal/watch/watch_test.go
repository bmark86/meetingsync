package watch

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"meetingsync/internal/domain"
	"meetingsync/internal/transcript"
)

const (
	wdir   = "/export"
	stable = 20 * time.Second
	srt    = "1\n00:00:01,000 --> 00:00:02,000\nHallo\n"
	md     = "Zusammenfassung\n\nEs ging um die Pipeline.\n"
)

var now = time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)

// lister baut einen Lister auf m; WarnOnce wie App.warnOnce (einmal je Schlüssel).
func lister(m *memFS) (Lister, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	warned := map[string]bool{}
	return Lister{
		FS:     m,
		Dir:    wdir,
		Stable: stable,
		Cache:  Cache{},
		Log:    logger,
		WarnOnce: func(key, format string, args ...any) {
			if warned[key] {
				return
			}
			warned[key] = true
			logger.Printf(format, args...)
		},
	}, &buf
}

func names(es []*Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

func byName(es []*Entry, name string) *Entry {
	for _, e := range es {
		if e.Name == name {
			return e
		}
	}
	return nil
}

func mustList(t *testing.T, l Lister, at time.Time) []*Entry {
	t.Helper()
	es, err := l.List(at)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// Fängt: Stabilität mit > statt >= stable_seconds; leere Datei gilt als
// stabil; instabile Datei wird schon gelesen (Inhalt/Typ gesetzt).
func TestListStability(t *testing.T) {
	m := newMemFS()
	m.add(wdir+"/jung.srt", srt, now.Add(-stable+time.Second))
	m.add(wdir+"/grenze.srt", srt, now.Add(-stable))
	m.add(wdir+"/leer.srt", "", now.Add(-time.Hour))
	m.add(wdir+"/notiz.md", md, now.Add(-time.Hour))
	l, logs := lister(m)

	es := mustList(t, l, now)
	if got := strings.Join(names(es), ","); got != "grenze.srt,jung.srt,leer.srt,notiz.md" {
		t.Fatalf("Einträge = %s", got)
	}
	for _, tc := range []struct {
		name   string
		stable bool
		kind   domain.Kind
	}{
		{"jung.srt", false, domain.KindUnsupported},
		{"grenze.srt", true, domain.KindTranscript},
		{"leer.srt", false, domain.KindUnsupported},
		{"notiz.md", true, domain.KindSummary},
	} {
		e := byName(es, tc.name)
		if e.Stable != tc.stable || e.Kind != tc.kind || (e.Content != nil) != tc.stable {
			t.Errorf("%s: Stable=%v Kind=%v Content=%v, want Stable=%v Kind=%v", tc.name, e.Stable, e.Kind, e.Content != nil, tc.stable, tc.kind)
		}
		if e.Path != filepath.Join(wdir, tc.name) {
			t.Errorf("%s: Path = %s", tc.name, e.Path)
		}
	}
	if got := strings.Join(m.reads, ","); got != wdir+"/grenze.srt,"+wdir+"/notiz.md" {
		t.Fatalf("gelesen: %s, want nur die stabilen", got)
	}
	if want := "Neu: grenze.srt → Transkript\nNeu: notiz.md → Zusammenfassung\n"; logs.String() != want {
		t.Fatalf("Log =\n%s\nwant\n%s", logs.String(), want)
	}

	// eine Sekunde später ist auch jung.srt stabil
	if e := byName(mustList(t, l, now.Add(time.Second)), "jung.srt"); !e.Stable || e.Kind != domain.KindTranscript {
		t.Fatalf("jung.srt nach stable_seconds: %+v", e)
	}
}

// Fängt: Grenze 20 MB verschoben; Hinweis je Durchlauf statt einmal.
func TestListTooLargeSkippedOnce(t *testing.T) {
	m := newMemFS()
	m.add(wdir+"/gross.srt", srt, now.Add(-time.Hour)).size = 20<<20 + 1
	m.add(wdir+"/genau.srt", srt, now.Add(-time.Hour)).size = 20 << 20
	l, logs := lister(m)
	for range 3 {
		if got := strings.Join(names(mustList(t, l, now)), ","); got != "genau.srt" {
			t.Fatalf("Einträge = %s, want genau.srt", got)
		}
	}
	if n := strings.Count(logs.String(), "Ignoriere gross.srt (größer als 20 MB)"); n != 1 {
		t.Fatalf("Hinweis %d-mal, want 1\n%s", n, logs.String())
	}
}

// Fängt: Endung der Temp-Namen fehlt oder Groß-/Kleinschreibung zählt;
// Präfix statt Endung geprüft.
func TestIsTempName(t *testing.T) {
	for name, want := range map[string]bool{
		"a.tmp": true, "A.TMP": true, "a.srt.part": true, "a.crdownload": true,
		"a.download": true, "a.swp": true, "a.Swp": true,
		"a.srt": false, "tmp.srt": false, "a.tmpx": false, "a.part.srt": false, "": false,
	} {
		if got := isTempName(name); got != want {
			t.Errorf("isTempName(%q) = %v, want %v", name, got, want)
		}
	}
}

// Temp-Namen, versteckte Dateien, Office-Sperrdateien und Ordner fallen still
// heraus (kein Hinweis, nicht gelesen).
// Fängt: Temp-Download wird verarbeitet (halbe Datei); Hinweis-Flut im Log.
func TestListSkipsTempHiddenAndDirs(t *testing.T) {
	m := newMemFS()
	for _, n := range []string{"a.srt.tmp", "b.srt.part", "c.srt.crdownload", "d.srt.download", ".e.srt.swp", ".versteckt.srt", "~$sperre.md", "F.SRT.TMP"} {
		m.add(wdir+"/"+n, srt, now.Add(-time.Hour))
	}
	m.add(wdir+"/ordner.srt", "", now.Add(-time.Hour)).mode = fs.ModeDir
	m.add(wdir+"/unter/x.srt", srt, now.Add(-time.Hour)) // nicht rekursiv
	l, logs := lister(m)
	if es := mustList(t, l, now); len(es) != 0 {
		t.Fatalf("Einträge = %v, want keine", names(es))
	}
	if logs.Len() != 0 || len(m.reads) != 0 {
		t.Fatalf("Log %q, gelesen %v; want nichts", logs.String(), m.reads)
	}
}

// Nicht reguläre Dateien (Symlink, FIFO, Gerät, Socket) mit unterstützter
// Endung: übersprungen, nicht gelesen, Hinweis einmal je Pfad über 3 Durchläufe.
// Fängt: Prüfung auf reguläre Datei fehlt (liest durch den Link, FIFO hängt);
// Hinweis per Printf statt WarnOnce.
func TestListSkipsNonRegularOnce(t *testing.T) {
	m := newMemFS()
	modes := map[string]fs.FileMode{"link.srt": fs.ModeSymlink, "pipe.srt": fs.ModeNamedPipe, "dev.srt": fs.ModeDevice, "sock.md": fs.ModeSocket}
	for n, mode := range modes {
		m.add(wdir+"/"+n, srt, now.Add(-time.Hour)).mode = mode
	}
	l, logs := lister(m)
	for range 3 {
		if es := mustList(t, l, now); len(es) != 0 {
			t.Fatalf("Einträge = %v, want keine", names(es))
		}
	}
	for n := range modes {
		if c := strings.Count(logs.String(), n+": keine reguläre Datei (z. B. Symlink) → übersprungen\n"); c != 1 {
			t.Errorf("%s: Hinweis %d-mal, want 1", n, c)
		}
	}
	if len(m.reads) != 0 {
		t.Fatalf("gelesen: %v", m.reads)
	}
}

// Fängt: nicht unterstützte Endung wird gelesen; konfigurierte Endung
// (transcript_extensions) nicht berücksichtigt; Hinweis mehrfach.
func TestListUnsupportedExtension(t *testing.T) {
	m := newMemFS()
	m.add(wdir+"/bild.PNG", "x", now.Add(-time.Hour))
	m.add(wdir+"/mitschrift.log", "irgendwas\n", now.Add(-time.Hour))
	l, logs := lister(m)
	l.Opts = transcript.Options{TranscriptExtensions: []string{".log"}}
	for range 2 {
		es := mustList(t, l, now)
		if len(es) != 1 || es[0].Name != "mitschrift.log" || es[0].Kind != domain.KindTranscript {
			t.Fatalf("Einträge = %v", names(es))
		}
	}
	if n := strings.Count(logs.String(), "Ignoriere bild.PNG (Dateityp .png wird nicht verarbeitet)\n"); n != 1 {
		t.Fatalf("Hinweis %d-mal, want 1\n%s", n, logs.String())
	}
}

// Typ wird je (mtime, Größe) einmal erkannt und gemeldet.
// Fängt: "Neu:" bei jedem Durchlauf; Cache ignoriert mtime-Änderung.
func TestListClassifyCache(t *testing.T) {
	m := newMemFS()
	f := m.add(wdir+"/a.txt", "Sprecher 1: Hallo\nSprecher 2: Hi\nSprecher 1: Gut\n", now.Add(-time.Hour))
	l, logs := lister(m)
	if e := mustList(t, l, now)[0]; e.Kind != domain.KindTranscript {
		t.Fatalf("Kind = %v", e.Kind)
	}
	// gleiche Größe und mtime, anderer Inhalt: Typ aus dem Cache
	f.data = []byte(strings.Repeat("x", len(f.data)))
	if e := mustList(t, l, now)[0]; e.Kind != domain.KindTranscript {
		t.Fatalf("Kind aus Cache = %v", e.Kind)
	}
	if n := strings.Count(logs.String(), "Neu: a.txt"); n != 1 {
		t.Fatalf("Neu %d-mal, want 1", n)
	}
	f.modTime = f.modTime.Add(time.Second)
	if e := mustList(t, l, now)[0]; e.Kind != domain.KindSummary {
		t.Fatalf("Kind nach Änderung = %v, want summary", e.Kind)
	}
	if !strings.HasSuffix(logs.String(), "Neu: a.txt → Zusammenfassung\n") {
		t.Fatalf("Log:\n%s", logs.String())
	}
	if c := l.Cache[wdir+"/a.txt"]; c.kind != domain.KindSummary || !c.modTime.Equal(f.modTime) {
		t.Fatalf("Cache = %+v", c)
	}
}

// Typ im Cache (gleiche mtime und Größe): nicht gelesen, Content nil; Fehlschlag
// (neue mtime) liest wieder (R-M5-01).
// Fängt: List liest jede stabile Datei in jedem Durchlauf.
func TestListCacheHitNoRead(t *testing.T) {
	m := newMemFS()
	f := m.add(wdir+"/a.srt", srt, now.Add(-time.Hour))
	l, _ := lister(m)
	if e := mustList(t, l, now)[0]; e.Content == nil || len(m.reads) != 1 {
		t.Fatalf("erster Durchlauf: Content=%v, gelesen %v", e.Content != nil, m.reads)
	}
	for range 3 {
		e := mustList(t, l, now)[0]
		if e.Content != nil || !e.Stable || e.Kind != domain.KindTranscript || e.Size != int64(len(srt)) {
			t.Fatalf("Cache-Treffer: %+v", e)
		}
	}
	if len(m.reads) != 1 {
		t.Fatalf("gelesen %v, want einmal", m.reads)
	}
	f.modTime = f.modTime.Add(time.Second)
	if e := mustList(t, l, now)[0]; string(e.Content) != srt || len(m.reads) != 2 {
		t.Fatalf("nach Änderung: Content=%q, gelesen %v", e.Content, m.reads)
	}
}

// Fängt: ReadDir-Fehler verschluckt; Info- oder Lesefehler bricht den
// Durchlauf ab statt die Datei zu überspringen.
func TestListErrors(t *testing.T) {
	m := newMemFS()
	m.dirErr = errors.New("weg")
	l, logs := lister(m)
	if es, err := l.List(now); err == nil || es != nil {
		t.Fatalf("List = %v, %v; want Fehler", es, err)
	}
	m.dirErr = nil
	m.add(wdir+"/info.srt", srt, now.Add(-time.Hour))
	m.add(wdir+"/lesen.srt", srt, now.Add(-time.Hour))
	m.add(wdir+"/gut.srt", srt, now.Add(-time.Hour))
	m.infoErr[wdir+"/info.srt"] = errors.New("lstat")
	m.readErr[wdir+"/lesen.srt"] = errors.New("read")
	if got := strings.Join(names(mustList(t, l, now)), ","); got != "gut.srt" {
		t.Fatalf("Einträge = %s, want gut.srt", got)
	}
	if strings.Contains(logs.String(), "info.srt") || strings.Contains(logs.String(), "lesen.srt") {
		t.Fatalf("Log:\n%s", logs.String())
	}
}

func ent(name string, mtime time.Time) *Entry {
	return &Entry{Path: wdir + "/" + name, Name: name, ModTime: mtime}
}

// Fängt: gleicher Name nicht bevorzugt; Name case-sensitiv; Fenster mit <
// statt <=; nicht die zeitlich nächste; vergebene Zusammenfassung erneut.
func TestPairSummary(t *testing.T) {
	const window = 180 * time.Second
	tr := ent("Meeting.srt", now)
	same := ent("meeting.MD", now.Add(170*time.Second))
	close1 := ent("Notizen.md", now.Add(-10*time.Second))
	close2 := ent("Andere.md", now.Add(20*time.Second))
	edge := ent("Rand.md", now.Add(window))
	far := ent("Weit.md", now.Add(window+time.Second))

	for _, tc := range []struct {
		name      string
		summaries []*Entry
		claimed   map[string]bool
		want      *Entry
	}{
		{"Name vor Zeit", []*Entry{close1, same}, nil, same},
		{"nächste in Zeit", []*Entry{close2, far, close1}, nil, close1},
		{"Fenstergrenze", []*Entry{far, edge}, nil, edge},
		{"außerhalb", []*Entry{far}, nil, nil},
		{"Name vergeben", []*Entry{same, close2}, map[string]bool{same.Path: true}, close2},
		{"Zeit vergeben", []*Entry{close1, close2}, map[string]bool{close1.Path: true}, close2},
		{"leer", nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PairSummary(tr, tc.summaries, tc.claimed, window); got != tc.want {
				t.Fatalf("PairSummary = %v, want %v", got, tc.want)
			}
		})
	}
	// Name zählt auch außerhalb des Fensters
	if got := PairSummary(tr, []*Entry{ent("Meeting.txt", now.Add(time.Hour))}, nil, window); got == nil {
		t.Fatal("gleicher Name außerhalb des Fensters nicht gepaart")
	}
}

// Fängt: nur in eine Richtung geprüft; Grenze mit < statt <=.
func TestNear(t *testing.T) {
	const window = time.Minute
	list := []*Entry{ent("a.srt", now.Add(-window)), ent("b.srt", now.Add(10*time.Minute))}
	for _, tc := range []struct {
		at   time.Time
		want bool
	}{
		{now, true},
		{now.Add(-2 * window), true},
		{now.Add(time.Second), false},
		{now.Add(10*time.Minute + window), true},
		{now.Add(-2*window - time.Second), false},
	} {
		if got := Near(list, tc.at, window); got != tc.want {
			t.Errorf("Near(%s) = %v, want %v", tc.at.Sub(now), got, tc.want)
		}
	}
	if Near(nil, now, window) {
		t.Fatal("Near(nil) = true")
	}
}

// OS liest das echte Dateisystem; Lstat und DirEntry folgen keinem Symlink.
func TestOSFS(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.srt"), srt)
	if err := os.Symlink(filepath.Join(dir, "a.srt"), filepath.Join(dir, "b.srt")); err != nil {
		t.Fatal(err)
	}
	var fsys FS = OS{}
	des, err := fsys.ReadDir(dir)
	if err != nil || len(des) != 2 || des[0].Name() != "a.srt" || !des[0].Type().IsRegular() || des[1].Type()&fs.ModeSymlink == 0 {
		t.Fatalf("ReadDir = %v, %v", des, err)
	}
	if b, err := fsys.ReadFile(filepath.Join(dir, "a.srt")); err != nil || string(b) != srt {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	if fi, err := fsys.Lstat(filepath.Join(dir, "b.srt")); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("Lstat = %v, %v", fi, err)
	}
}

// Der Lister auf dem echten Dateisystem: Symlink übersprungen, Datei erkannt.
func TestListOnOS(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.srt")
	writeFile(t, p, srt)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(p, filepath.Join(dir, "b.srt")); err != nil {
		t.Fatal(err)
	}
	l, logs := lister(nil)
	l.FS, l.Dir = OS{}, dir
	es := mustList(t, l, time.Now())
	if len(es) != 1 || es[0].Name != "a.srt" || !es[0].Stable || es[0].Kind != domain.KindTranscript || string(es[0].Content) != srt {
		t.Fatalf("Einträge = %+v", es)
	}
	if !strings.Contains(logs.String(), "b.srt: keine reguläre Datei (z. B. Symlink) → übersprungen") {
		t.Fatalf("Log:\n%s", logs.String())
	}
}

// Sync des Zielordners scheitert: dst und *DirSyncError (Text, Unwrap),
// Quelle trotzdem verschoben.
func TestMoveDirSyncError(t *testing.T) {
	cause := errors.New("EIO")
	m := Mover{Rename: noRename, SyncDir: func(string) error { return cause }}
	src, dir := moveEnv(t)
	dst, err := m.Move(src, dir)
	var se *DirSyncError
	if !errors.As(err, &se) || !errors.Is(err, cause) {
		t.Fatalf("err = %v, want *DirSyncError", err)
	}
	if want := fmt.Sprintf("Zielordner nicht synchronisiert, trotzdem verschoben nach %s: EIO", dst); err.Error() != want {
		t.Fatalf("Text = %q, want %q", err.Error(), want)
	}
	checkMoved(t, src, dst, "a.txt")
}

// Echter fsync des Ordners: fehlender Ordner liefert einen Fehler.
func TestSyncDir(t *testing.T) {
	if err := syncDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := syncDir(filepath.Join(t.TempDir(), "fehlt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

// Fängt: Seen ohne übersprungene Dateien (Warnung käme je Durchlauf neu), mit
// Unterordnern oder gefüllt trotz ReadDir-Fehler.
func TestListSeen(t *testing.T) {
	m := newMemFS()
	m.add(wdir+"/a.srt", srt, now.Add(-time.Hour))
	m.add(wdir+"/jung.srt", srt, now)
	m.add(wdir+"/x.pdf", "x", now.Add(-time.Hour))
	m.add(wdir+"/.versteckt", "x", now.Add(-time.Hour))
	m.add(wdir+"/link.srt", srt, now.Add(-time.Hour)).mode = fs.ModeSymlink
	m.add(wdir+"/_archiv", "", now.Add(-time.Hour)).mode = fs.ModeDir
	m.add(wdir+"/_archiv/b.srt", srt, now.Add(-time.Hour))
	l, _ := lister(m)
	l.Seen = map[string]bool{}
	mustList(t, l, now)
	var got []string
	for p := range l.Seen {
		got = append(got, p)
	}
	slices.Sort(got)
	want := []string{wdir + "/.versteckt", wdir + "/a.srt", wdir + "/jung.srt", wdir + "/link.srt", wdir + "/x.pdf"}
	if !slices.Equal(got, want) {
		t.Fatalf("Seen = %v, want %v", got, want)
	}

	m.dirErr = errors.New("weg")
	l.Seen = map[string]bool{}
	if _, err := l.List(now); err == nil || len(l.Seen) != 0 {
		t.Fatalf("err %v, Seen %v; want Fehler, leer", err, l.Seen)
	}
}
