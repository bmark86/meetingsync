package main

import (
	"slices"
	"testing"
)

func TestNormExts(t *testing.T) {
	got := normExts([]string{".txt", "TXT", " .Txt ", "", "  ", "Md"})
	want := []string{".txt", ".txt", ".txt", ".md"}
	if !slices.Equal(got, want) {
		t.Fatalf("normExts = %q, want %q", got, want)
	}
	if got := normExts(nil); got == nil || len(got) != 0 {
		t.Fatalf("normExts(nil) = %#v, want leere Liste", got)
	}
}
