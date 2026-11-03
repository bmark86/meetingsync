// Package ledger führt das Protokoll gegen Doppelverarbeitung (ADR 0004):
// JSON-Lines-Datei, pro Hash gilt die letzte Zeile, Anhängen mit fsync.
package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Status eines Ledger-Eintrags (ADR 0004). Pro Hash gilt die letzte Zeile.
// Leerer Status (Zeilen von v1.0.0, Add ohne Status) bedeutet fertig.
const (
	StatusBegonnen  = "begonnen"  // Verarbeitung gestartet, Notion-Seite evtl. angelegt
	StatusFertig    = "fertig"    // Notiz angelegt
	StatusVerworfen = "verworfen" // nach _fehler verschoben, darf neu laufen
)

// Entry ist ein Protokolleintrag zu einem Datei-Hash.
type Entry struct {
	Hash    string
	Status  string
	File    string
	ModTime time.Time
	URL     string
	At      time.Time
	// Nur bei fertig, 0 im Normalfall (R-M1-03, OD-04: nur protokolliert);
	// nur für das Schreiben, Load liest sie nicht zurück.
	MissingTasks int // nicht angelegte To-dos
	BlockErrors  int // Fehler beim Anhängen der restlichen Blöcke
}

// ledgerLine ist das Zeilenformat auf Platte. Fertige Zeilen tragen den Hash
// in "hash" und keinen Status, byte-gleich zu v1.0.0. Alle anderen Zeilen
// tragen ihn in "sha256": v1.0.0 wertet jede Zeile mit "hash" als verarbeitet
// und würde sonst eine begonnene Datei ohne Notiz archivieren.
type ledgerLine struct {
	Hash    string    `json:"hash,omitempty"`
	SHA256  string    `json:"sha256,omitempty"`
	Status  string    `json:"status,omitempty"`
	File    string    `json:"file"`
	ModTime time.Time `json:"mtime"`
	URL     string    `json:"url"`
	At      time.Time `json:"at"`
	// omitempty: ohne Lücken bleibt die fertig-Zeile byte-gleich zu v1.0.0;
	// v1.0.0 ignoriert die Zusatzfelder beim Lesen.
	TodosFehlen   int `json:"todos_fehlen,omitempty"`
	BloeckeFehler int `json:"bloecke_fehler,omitempty"`
}

// Ledger ist das Protokoll gegen Doppelverarbeitung (ADR 0004).
type Ledger struct {
	path    string
	persist bool
	byHash  map[string]Entry
}

// put übernimmt e als letzten Stand für e.Hash.
func (l *Ledger) put(e Entry) Entry {
	if e.Status == "" {
		e.Status = StatusFertig
	}
	l.byHash[e.Hash] = e
	return e
}

// Load liest die Datei path (fehlt sie, startet das Ledger leer). Ist persist
// false, schreibt Add nichts auf die Platte.
func Load(path string, persist bool) (*Ledger, error) {
	l := &Ledger{path: path, persist: persist, byHash: map[string]Entry{}}
	f, err := os.Open(path) //nolint:gosec // G304: Pfad des Protokolls unter archive_dir aus der eigenen Konfiguration
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // nur gelesen, Close-Fehler ohne Folgen
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var ln ledgerLine
		if json.Unmarshal(sc.Bytes(), &ln) != nil {
			continue
		}
		hash := ln.Hash
		if hash == "" {
			hash = ln.SHA256
		}
		if hash == "" {
			continue
		}
		l.put(Entry{Hash: hash, Status: ln.Status, File: ln.File, ModTime: ln.ModTime, URL: ln.URL, At: ln.At})
	}
	return l, sc.Err()
}

// Has meldet, ob der letzte Eintrag zu hash fertig ist.
func (l *Ledger) Has(hash string) bool { return l.byHash[hash].Status == StatusFertig }

// Started meldet, ob der letzte Eintrag zu hash "begonnen" ist.
func (l *Ledger) Started(hash string) bool { return l.byHash[hash].Status == StatusBegonnen }

// HasNear meldet, ob ein fertiger Eintrag eine mtime im Fenster um t hat.
func (l *Ledger) HasNear(t time.Time, window time.Duration) bool {
	for _, e := range l.byHash {
		if e.Status == StatusFertig && absDur(e.ModTime.Sub(t)) <= window {
			return true
		}
	}
	return false
}

// Add übernimmt e sofort in den Speicher und hängt danach eine Zeile an die
// Datei an (mit fsync, beim ersten Anlegen auch des Ordners). Fehler von
// Write, Sync und Close kommen zurück; der Eintrag im Speicher bleibt dann
// trotzdem stehen.
func (l *Ledger) Add(e Entry) error {
	e = l.put(e)
	if !l.persist {
		return nil
	}
	ln := ledgerLine{File: e.File, ModTime: e.ModTime, URL: e.URL, At: e.At, TodosFehlen: e.MissingTasks, BloeckeFehler: e.BlockErrors}
	if e.Status == StatusFertig {
		ln.Hash = e.Hash
	} else {
		ln.SHA256, ln.Status = e.Hash, e.Status
	}
	b, err := json.Marshal(ln)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	_, statErr := os.Lstat(l.path)
	created := errors.Is(statErr, os.ErrNotExist)
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	line := append(b, '\n')
	// Abgerissene letzte Zeile (Absturz beim Schreiben): erst '\n', sonst
	// verschmilzt die neue Zeile mit dem Rest und geht beim Laden verloren.
	info, err := f.Stat()
	if err == nil && info.Size() > 0 {
		last := []byte{0}
		if _, err = f.ReadAt(last, info.Size()-1); err == nil && last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	if err == nil {
		_, err = f.Write(line)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && created {
		err = syncDir(filepath.Dir(l.path)) // neuer Ordnereintrag soll den Absturz überstehen
	}
	return err
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: Ordner des Protokolls aus der eigenen Konfiguration, nur für fsync geöffnet
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
