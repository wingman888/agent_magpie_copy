package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/backup"
)

// backupCmd: `magpie backup [--no-keys] [file]`
func backupCmd(args []string) error {
	const usage = "usage: magpie backup [--no-keys] [file]"
	keys := true
	var files []string
	for _, a := range args {
		switch {
		case a == "--no-keys":
			keys = false
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s\n%s", a, usage)
		default:
			files = append(files, a)
		}
	}
	if len(files) > 1 {
		return errors.New(usage)
	}
	file := "magpie" + backup.Ext
	if len(files) == 1 {
		file = files[0]
	}
	pass, err := readPassphrase(true)
	if err != nil {
		return err
	}
	b, err := backup.Collect(keys, version)
	if err != nil {
		return err
	}
	data, err := backup.Seal(b, pass)
	if err != nil {
		return err
	}
	if err := writeSecret(file, data); err != nil {
		return err
	}
	with := "with their keys"
	if !keys {
		with = "without keys"
	}
	fmt.Println(green.Render("✓"), "saved", bold.Render(file),
		fmt.Sprintf("· %d providers %s · %d profiles · %d agent settings", len(b.Providers), with, len(b.Profiles), len(b.Agents)))
	fmt.Println(muted.Render("subscriptions are not in it: sign in to them on the other machine · keep the passphrase safe, it cannot be recovered"))
	return nil
}

// restoreCmd: `magpie restore [--no-agents] <file>`
func restoreCmd(args []string) error {
	const usage = "usage: magpie restore [--no-agents] <file>"
	parts := backup.All
	var files []string
	for _, a := range args {
		switch {
		case a == "--no-agents":
			parts.Agents = false
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s\n%s", a, usage)
		default:
			files = append(files, a)
		}
	}
	if len(files) != 1 {
		return errors.New(usage)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		return err
	}
	pass, err := readPassphrase(false)
	if err != nil {
		return err
	}
	b, err := backup.Open(data, pass)
	if err != nil {
		return err
	}
	r, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}
	settingsNote := "settings not restored"
	if r.Settings {
		settingsNote = "settings restored"
	}
	fmt.Println(green.Render("✓"), "restored",
		fmt.Sprintf("%d providers added, %d replaced · %s · %d profiles · %d agent settings changed", r.Added, r.Replaced, settingsNote, r.Profiles, r.Agents))
	if len(r.NeedKey) > 0 {
		fmt.Println(amber.Render("!"), "no key yet · magpie provider key <id>:", strings.Join(r.NeedKey, ", "))
	}
	if len(r.Skipped) > 0 {
		fmt.Println(amber.Render("!"), "left as they were:", strings.Join(r.Skipped, ", "))
	}
	return nil
}

// readPassphrase asks for the passphrase without echo (twice when confirm),
// or takes the first line of a piped stdin.
func readPassphrase(confirm bool) (string, error) {
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		pass := strings.TrimRight(line, "\r\n")
		if pass == "" {
			return "", errors.New("the passphrase is empty")
		}
		return pass, nil
	}
	pass, err := promptHidden("Passphrase: ")
	if err != nil {
		return "", err
	}
	if pass == "" {
		return "", errors.New("the passphrase is empty")
	}
	if confirm {
		again, err := promptHidden("Again: ")
		if err != nil {
			return "", err
		}
		if again != pass {
			return "", errors.New("the passphrases differ")
		}
	}
	return pass, nil
}

// promptHidden reads a line from the terminal with echo off.
func promptHidden(msg string) (string, error) {
	fmt.Fprint(os.Stderr, msg)
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	hidden := off.Run() == nil
	if hidden {
		defer func() {
			on := exec.Command("stty", "echo")
			on.Stdin = os.Stdin
			_ = on.Run()
			fmt.Fprintln(os.Stderr)
		}()
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// writeSecret writes the file whole or not at all, readable by its owner only.
func writeSecret(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".magpie-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

