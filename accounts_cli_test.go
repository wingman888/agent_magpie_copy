package main

import (
	"strings"
	"testing"
	"time"
)

func TestUntilShort(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Minute:                  "now",
		45 * time.Minute:              "45m",
		2*time.Hour + 13*time.Minute:  "2h13m",
		76*time.Hour + 30*time.Minute: "3d4h",
	} {
		if got := untilShort(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestQuotaCell(t *testing.T) {
	at := time.Now().Add(2*time.Hour + 13*time.Minute + 30*time.Second)
	got := quotaCell(quotaSpan{Name: "5 hours", Used: 42, ResetsAt: &at})
	if !strings.HasPrefix(got, "5h 42%") || !strings.Contains(got, "↻2h13m") {
		t.Fatalf("%q", got)
	}
}
