package main

import (
	"testing"

	"meetingsync/internal/config"
)

// Die Tests der Wiederholungsregel liegen in internal/pipeline/retry_test.go.

func TestDefaultMaxAttempts(t *testing.T) {
	if got := config.Default().MaxAttempts; got != 10 {
		t.Fatalf("MaxAttempts = %d, want 10", got)
	}
}
