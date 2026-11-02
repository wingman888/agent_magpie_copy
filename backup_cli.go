package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/yetone/magpie/internal/backup"
)

const (
	backupUsage  = "usage: magpie backup [--no-keys] [file]   (default file: magpie" + backup.Ext + ")"
	restoreUsage = "usage: magpie restore [--no-agents] <file>"
)

// parseBackupArgs reads `magpie backup [--no-keys] [file]`.
func parseBackupArgs(args []string) (noKeys bool, file string, err error) {
	for _, a := range args {
		switch {
		case a == "--no-keys":
			noKeys = true
		case strings.HasPrefix(a, "-"):
			return false, "", fmt.Errorf("unknown option %s\n%s", a, backupUsage)
		case file != "":
			return false, "", fmt.Errorf("too many arguments\n%s", backupUsage)
		default:
			file = a
		}
	}
	if file == "" {
		file = "magpie" + backup.Ext
	}
	return noKeys, file, nil
}

// parseRestoreArgs reads `magpie restore [--no-agents] <file>`.
func parseRestoreArgs(args []string) (noAgents bool, file string, err error) {
	for _, a := range args {
		switch {
		case a == "--no-agents":
			noAgents = true
		case strings.HasPrefix(a, "-"):
			return false, "", fmt.Errorf("unknown option %s\n%s", a, restoreUsage)
		case file != "":
			return false, "", fmt.Errorf("too many arguments\n%s", restoreUsage)
		default:
			file = a
		}
	}
	if file == "" {
		return false, "", errors.New(restoreUsage)
	}
	return noAgents, file, nil
}

var errNoPass = errors.New("the passphrase is empty")

// firstLine is the first line of r, without its line ending.
func firstLine(r io.Reader) string {
	line, _ := bufio.NewReader(r).ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

func stdinTerminal() bool { return term.IsTerminal(os.Stdin.Fd()) }

func askHidden(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(os.Stdin.Fd())
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// passphrase asks for it on a terminal, without echo, twice when confirm is
// set; anywhere else it's the first line of standard input.
func passphrase(confirm bool) (string, error) {
	if !stdinTerminal() {
		p := firstLine(os.Stdin)
		if p == "" {
			return "", errNoPass
		}
		return p, nil
	}
	p, err := askHidden("Passphrase: ")
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", errNoPass
	}
	if confirm {
		again, err := askHidden("Again: ")
		if err != nil {
			return "", err
		}
		if again != p {
			return "", errors.New("the passphrases don't match; nothing written")
		}
	}
	return p, nil
}

// writePrivate writes data to path readable and writable by the owner only,
// through a temporary file so a failure leaves no half-written backup.
func writePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".magpie-backup-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return os.Chmod(path, 0o600)
}

func backupCmd(args []string) error {
	noKeys, file, err := parseBackupArgs(args)
	if err != nil {
		return err
	}
	pass, err := passphrase(true)
	if err != nil {
		return err
	}
	b, err := backup.Collect(!noKeys, version)
	if err != nil {
		return err
	}
	data, err := backup.Seal(b, pass)
	if err != nil {
		return err
	}
	if err := writePrivate(file, data); err != nil {
		return err
	}
	keys := "with keys"
	if noKeys {
		keys = "without keys"
	}
	fmt.Printf("%s %s: %d providers (%s), %d profiles, %d agent settings\n",
		green.Render("✓"), file, len(b.Providers), keys, len(b.Profiles), len(b.Agents))
	fmt.Println(muted.Render("  subscriptions are not in it: sign in to them on the other machine"))
	fmt.Println(muted.Render("  without the passphrase the file can't be opened: keep it somewhere safe"))
	return nil
}

func restoreCmd(args []string) error {
	noAgents, file, err := parseRestoreArgs(args)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	// a file that isn't a backup, or is too new, is told before asking
	// for anything
	if _, err := backup.Open(data, ""); errors.Is(err, backup.ErrNotBackup) || errors.Is(err, backup.ErrTooNew) {
		return fmt.Errorf("%s: %w", file, err)
	}
	pass, err := passphrase(false)
	if err != nil {
		return err
	}
	b, err := backup.Open(data, pass)
	if err != nil {
		return err // nothing on this machine touched yet
	}
	parts := backup.All
	if noAgents {
		parts.Agents = false
	}
	res, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}
	settingsWord := "settings not in the backup"
	if res.Settings {
		settingsWord = "settings restored"
	}
	agents := fmt.Sprintf("%d agent settings changed", res.Agents)
	if noAgents {
		agents = "agents left alone"
	}
	fmt.Printf("%s restored %s: providers %d added, %d replaced · %s · %d profiles · %s\n",
		green.Render("✓"), file, res.Added, res.Replaced, settingsWord, res.Profiles, agents)
	if len(res.NeedKey) > 0 {
		fmt.Println(amber.Render("!"), "no key yet for:", strings.Join(res.NeedKey, ", "))
		fmt.Println(muted.Render("  add one with: magpie provider key <id> <key>"))
	}
	if len(res.Skipped) > 0 {
		fmt.Println(amber.Render("!"), "skipped (the agent isn't installed on this machine, or its model isn't available yet):")
		for _, s := range res.Skipped {
			fmt.Println("   ", s)
		}
	}
	fmt.Println(muted.Render("  agents read their settings when they start: restart running sessions to pick them up"))
	return nil
}
