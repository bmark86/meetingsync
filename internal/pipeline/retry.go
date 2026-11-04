package pipeline

import (
	"context"
	"time"

	"meetingsync/internal/domain"
)

// FileState ist der Wiederholungszustand einer Datei im Exportordner.
type FileState struct {
	Attempts  int       // Fehlversuche, zählen gegen max_attempts
	Transient int       // bisherige vorübergehende Fehler, steuern den Backoff
	NextTry   time.Time // vorher kein neuer Versuch
	// giveUp: max_attempts erreicht, Verschieben nach _fehler gescheitert;
	// Ursache für .fehler.txt beim nächsten Versuch (ohne LLM-Lauf), nur
	// solange der Inhalt giveUpHash hat
	giveUp     error
	giveUpHash string
}

const (
	// retryLaterWait: Wartezeit nach anderen Fehlern sowie nach gescheitertem
	// Archivieren oder Verschieben (Neustart-Regel).
	retryLaterWait = 2 * time.Minute
	// maxBackoffShift begrenzt den Backoff: 1 min << 4 = 16 min.
	maxBackoffShift = 4
)

// retryPolicy ist die Wiederholungsregel (ADR 0004): alle Fehler zählen
// gemeinsam gegen maxAttempts; vorübergehende Fehler warten 1, 2, 4, 8,
// 16 min, danach 16 min, andere 2 min. Ein vorübergehender Fehler bei
// abgebrochenem Kontext zählt nicht.
type retryPolicy struct{ maxAttempts int }

func newRetryPolicy(maxAttempts int) retryPolicy { return retryPolicy{maxAttempts: maxAttempts} }

// counts meldet, ob err als Versuch zählt.
func (r retryPolicy) counts(ctx context.Context, err error) bool {
	return !domain.IsTransient(err) || ctx.Err() == nil
}

// record verbucht einen gezählten Fehler in st. giveUp: max_attempts ist
// erreicht; sonst steht in st.NextTry der nächste Versuch (now + wait).
func (r retryPolicy) record(st *FileState, transient bool, now time.Time) (wait time.Duration, giveUp bool) {
	st.Attempts++
	if st.Attempts >= r.maxAttempts {
		return 0, true
	}
	wait = retryLaterWait
	if transient {
		wait = time.Minute << min(st.Transient, maxBackoffShift) // 1, 2, 4, 8, 16 min
		st.Transient++
	}
	st.NextTry = now.Add(wait)
	return wait, false
}

// state liefert den Zustand von path und legt ihn bei Bedarf an.
func (p *Pipeline) state(path string) *FileState {
	st := p.states[path]
	if st == nil {
		st = &FileState{}
		p.states[path] = st
	}
	return st
}

// retryLater: neuer Versuch für path erst in 2 min statt bei jedem Durchlauf
// (Logzeile und Verschieben nicht alle poll_interval_seconds). Zählt nicht
// gegen max_attempts.
func (p *Pipeline) retryLater(path string, now time.Time) {
	p.state(path).NextTry = now.Add(retryLaterWait)
}

// States liefert den Wiederholungszustand je Pfad (die Map selbst, keine
// Kopie). Nur aus der Goroutine des Durchlaufs benutzen.
func (p *Pipeline) States() map[string]*FileState { return p.states }
