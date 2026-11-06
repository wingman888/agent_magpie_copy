package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// stdinIsTerminal reports whether standard input is an interactive terminal.
func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// readLineFrom returns the first line of r without its line ending.
func readLineFrom(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readSecret reads one passphrase. On a terminal the prompt goes to stderr and
// the typing is not echoed; otherwise the first line of stdin is taken.
func readSecret(prompt string) (string, error) {
	if !stdinIsTerminal() {
		return readLineFrom(os.Stdin)
	}
	fmt.Fprint(os.Stderr, prompt)
	s, err := readHiddenLine(os.Stdin)
	fmt.Fprintln(os.Stderr)
	return s, err
}

// askPassphrase reads the passphrase. When confirm is set and a terminal is in
// use, it is typed twice and both must match. An empty passphrase is refused.
func askPassphrase(confirm bool) (string, error) {
	pass, err := readSecret("口令：")
	if err != nil {
		return "", err
	}
	if pass == "" {
		return "", errors.New("口令不能为空")
	}
	if confirm && stdinIsTerminal() {
		again, err := readSecret("再输入一次口令：")
		if err != nil {
			return "", err
		}
		if again != pass {
			return "", errors.New("两次输入的口令不一致")
		}
	}
	return pass, nil
}
