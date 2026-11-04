package ledger

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// ErrLocked meldet, dass ein anderer Lauf die Sperre hält.
var ErrLocked = errors.New("Sperre belegt")

// Lock ist die Sperre gegen gleichzeitige Läufe (Dienst und -file, R-M3-14):
// ein flock auf den Ordner dir (der Exportordner). Auf den Ordner selbst,
// damit keine Sperrdatei im Export- oder Archivordner entsteht. Die Sperre
// gilt nur unter Prozessen, die sie ebenfalls nehmen (advisory).
type Lock struct{ f *os.File }

// TryLock nimmt die Sperre auf dir ohne zu warten; hält sie ein anderer,
// kommt ErrLocked.
func TryLock(dir string) (*Lock, error) {
	f, err := os.Open(dir) //nolint:gosec // G304: Exportordner aus der eigenen Konfiguration, nur für flock geöffnet
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // G115: Dateideskriptor passt in int
		_ = f.Close() //nolint:errcheck // Sperrfehler zählt, Close-Fehler ohne Folgen
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &Lock{f: f}, nil
}

// WaitLock nimmt die Sperre auf dir und wartet, bis sie frei ist: neuer
// Versuch alle poll, bis ctx endet. waiting läuft einmal, wenn gewartet wird.
func WaitLock(ctx context.Context, dir string, poll time.Duration, waiting func()) (*Lock, error) {
	for told := false; ; told = true {
		l, err := TryLock(dir)
		if !errors.Is(err, ErrLocked) {
			return l, err
		}
		if !told {
			waiting()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Unlock gibt die Sperre frei (Schließen beendet den flock).
func (l *Lock) Unlock() error { return l.f.Close() }
