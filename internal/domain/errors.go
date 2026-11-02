package domain

import (
	"context"
	"errors"
)

// --- Fehlerklassen -------------------------------------------------------------

type transientError struct{ err error }

func (e transientError) Error() string { return e.err.Error() }
func (e transientError) Unwrap() error { return e.err }

// Transient markiert Fehler, bei denen sich Warten lohnt (LM Studio aus,
// kein Netz, Rate-Limit). Solche Dateien bleiben liegen und werden später erneut versucht.
func Transient(err error) error { return transientError{err} }

// IsTransient meldet, ob err (auch eingewickelt) als vorübergehend markiert ist.
// Abbruch und Zeitüberschreitung des Kontexts zählen ebenfalls als vorübergehend.
func IsTransient(err error) bool {
	var t transientError
	return errors.As(err, &t) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
