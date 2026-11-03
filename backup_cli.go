package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/yetone/magpie/internal/backup"
)

const (
	defaultBackupFile = "magpie" + backup.Ext

	backupUsageText  = "usage: magpie backup [--no-keys] [file]     file defaults to " + defaultBackupFile
	restoreUsageText = "usage: magpie restore [--no-agents] <file>"
)

var errEmptyPassphrase = errors.New("口令不能为空")

// parseBackupArgs reads the arguments after `magpie backup`.
func parseBackupArgs(args []string) (noKeys bool, file string, err error) {
	for _, a := range args {
		switch {
		case a == "--no-keys":
			noKeys = true
		case strings.HasPrefix(a, "-"):
			return false, "", fmt.Errorf("unknown option %q\n%s", a, backupUsageText)
		case file != "":
			return false, "", fmt.Errorf("too many arguments\n%s", backupUsageText)
		default:
			file = a
		}
	}
	if file == "" {
		file = defaultBackupFile
	}
	return noKeys, file, nil
}

// parseRestoreArgs reads the arguments after `magpie restore`.
func parseRestoreArgs(args []string) (noAgents bool, file string, err error) {
	for _, a := range args {
		switch {
		case a == "--no-agents":
			noAgents = true
		case strings.HasPrefix(a, "-"):
			return false, "", fmt.Errorf("unknown option %q\n%s", a, restoreUsageText)
		case file != "":
			return false, "", fmt.Errorf("too many arguments\n%s", restoreUsageText)
		default:
			file = a
		}
	}
	if file == "" {
		return false, "", fmt.Errorf("missing backup file\n%s", restoreUsageText)
	}
	return noAgents, file, nil
}

// readPassphrase returns the passphrase without ever printing it. On a
// terminal it is read through secret (no echo), twice when confirm is set and
// the two must match. Otherwise it is the first line of in, without its line
// ending, and is not confirmed.
func readPassphrase(secret func(prompt string) (string, error), isTerminal bool, in io.Reader, confirm bool) (string, error) {
	if !isTerminal {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return "", errEmptyPassphrase
		}
		return line, nil
	}
	first, err := secret("口令: ")
	if err != nil {
		return "", err
	}
	if first == "" {
		return "", errEmptyPassphrase
	}
	if confirm {
		again, err := secret("再输入一次口令: ")
		if err != nil {
			return "", err
		}
		if again != first {
			return "", errors.New("两次输入的口令不一致")
		}
	}
	return first, nil
}

// stdinPassphrase reads the passphrase from the real standard input.
func stdinPassphrase(confirm bool) (string, error) {
	fd := os.Stdin.Fd()
	isTerminal := term.IsTerminal(fd)
	secret := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	return readPassphrase(secret, isTerminal, os.Stdin, confirm)
}

// writeBackupFile writes the sealed backup readable and writable by its owner only.
func writeBackupFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// backupCmd: `magpie backup [--no-keys] [file]`
func backupCmd(args []string) error {
	noKeys, file, err := parseBackupArgs(args[1:])
	if err != nil {
		return err
	}
	pass, err := stdinPassphrase(true)
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
	if err := writeBackupFile(file, data); err != nil {
		return err
	}

	withKey := 0
	for _, p := range b.Providers {
		if p.Key != "" || len(p.Keys) > 0 {
			withKey++
		}
	}
	fmt.Println(green.Render("✓"), "backed up to", bold.Render(file),
		muted.Render(fmt.Sprintf("· %d providers (%d with keys, %d without) · %d profiles · %d agent settings",
			len(b.Providers), withKey, len(b.Providers)-withKey, len(b.Profiles), len(b.Agents))))
	fmt.Println(muted.Render("  subscriptions are not in the backup: sign in again on the other machine"))
	fmt.Println(muted.Render("  without the passphrase the file cannot be opened"))
	return nil
}

// restoreCmd: `magpie restore [--no-agents] <file>`
func restoreCmd(args []string) error {
	noAgents, file, err := parseRestoreArgs(args[1:])
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	// Check the format before asking for a passphrase: a file that is not a
	// backup, or comes from a newer magpie, is reported as such.
	if _, err := backup.Open(data, ""); errors.Is(err, backup.ErrNotBackup) || errors.Is(err, backup.ErrNewer) {
		return fmt.Errorf("%s: %w", file, err)
	}
	pass, err := stdinPassphrase(false)
	if err != nil {
		return err
	}
	b, err := backup.Open(data, pass)
	switch {
	case errors.Is(err, backup.ErrPassphrase):
		return fmt.Errorf("%w，本机配置未改动", backup.ErrPassphrase)
	case errors.Is(err, backup.ErrNotBackup), errors.Is(err, backup.ErrNewer):
		return fmt.Errorf("%s: %w", file, err)
	case err != nil:
		return err
	}

	parts := backup.All
	parts.Agents = !noAgents
	res, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}

	fmt.Println(green.Render("✓"), "restored from", bold.Render(file))
	fmt.Printf("  providers: %d added, %d replaced\n", res.Added, res.Replaced)
	if res.Settings {
		fmt.Println("  settings: restored")
	} else {
		fmt.Println(muted.Render("  settings: not in the backup, kept this machine's"))
	}
	fmt.Printf("  profiles: %d restored\n", res.Profiles)
	if noAgents {
		fmt.Println(muted.Render("  agents: left alone (--no-agents)"))
	} else {
		fmt.Printf("  agents: %d changed\n", res.Agents)
	}
	if len(res.NeedKey) > 0 {
		ids := providerIDsByName(b)
		fmt.Println(amber.Render("!"), "these providers have no key yet:")
		for _, name := range res.NeedKey {
			id := ids[name]
			if id == "" {
				id = name
			}
			fmt.Printf("  %s  %s\n", bold.Render(name), muted.Render("· magpie provider key "+id))
		}
	}
	for _, s := range res.Skipped {
		fmt.Println(amber.Render("!"), "skipped", s)
	}
	return nil
}

// providerIDsByName maps the name a provider is listed under to its id, the
// same name Restore reports in NeedKey.
func providerIDsByName(b backup.Bundle) map[string]string {
	out := map[string]string{}
	for _, p := range b.Providers {
		name := p.Name
		if name == "" {
			name = p.ID
		}
		out[name] = strings.ToLower(strings.TrimSpace(p.ID))
	}
	return out
}
