package main

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"testing"
)

// /dev/null is a character device but not a terminal: a non-interactive stdin
// must refuse without reading and without saving.
func TestIsTerminalRejectsDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip("no /dev/null:", err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("/dev/null reported as a terminal")
	}
	if isTerminal(nil) {
		t.Fatal("nil file reported as a terminal")
	}
}

func TestImportDevNullStdinRefuses(t *testing.T) {
	isolateConfig(t)
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip("no /dev/null:", err)
	}
	defer f.Close()
	var out bytes.Buffer
	err = runImport([]string{testLink}, f, &out, isTerminal(f))
	if err == nil || !strings.Contains(err.Error(), "nothing added") {
		t.Fatalf("err = %v, want a nothing-added refusal", err)
	}
	if _, ok := savedKey(t, "test-relay"); ok {
		t.Fatal("import from /dev/null saved a provider")
	}
}

func TestReadConfirmLineBounds(t *testing.T) {
	// exactly the limit, with its newline: accepted
	line := strings.Repeat("y", maxConfirmLine)
	got, err := readConfirmLine(bufio.NewReader(strings.NewReader(line + "\n")))
	if err != nil || got != line {
		t.Fatalf("limit-length line: got len %d, err %v", len(got), err)
	}
	// one byte over, with a newline: refused
	if _, err := readConfirmLine(bufio.NewReader(strings.NewReader(line + "y\n"))); err == nil {
		t.Fatal("over-long line accepted")
	}
	// over-long and never terminated: refused, and the read stops early
	long := strings.NewReader(strings.Repeat("y", 1<<20))
	if _, err := readConfirmLine(bufio.NewReader(long)); err == nil {
		t.Fatal("unterminated over-long input accepted")
	}
	if long.Len() < 1<<19 {
		t.Fatalf("reader consumed too much input: %d bytes left", long.Len())
	}
}

func TestImportOverlongAnswerRefuses(t *testing.T) {
	isolateConfig(t)
	var out bytes.Buffer
	// "y" repeated without a newline must not count as a yes.
	err := runImport([]string{testLink}, strings.NewReader(strings.Repeat("y", 4096)), &out, true)
	if err == nil || !strings.Contains(err.Error(), "nothing added") {
		t.Fatalf("err = %v, want a nothing-added refusal", err)
	}
	if _, ok := savedKey(t, "test-relay"); ok {
		t.Fatal("over-long answer saved a provider")
	}
}
