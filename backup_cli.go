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
	"github.com/yetone/magpie/internal/provider"
)

const backupUsage = `usage:
  magpie backup [--no-keys] [file]       save providers, settings, profiles and agent models
                                         into one passphrase-sealed file (default magpie.magpie-backup)
  magpie restore [--no-agents] <file>    restore a backup here; --no-agents leaves agent models alone`

const defaultBackupFile = "magpie" + backup.Ext

// passSource reads passphrases from the terminal without echo, or else from
// the first line of standard input when standard input is not a terminal.
type passSource struct {
	tty bool
	in  *bufio.Reader
}

func newPassSource() passSource {
	return passSource{
		tty: term.IsTerminal(os.Stdin.Fd()),
		in:  bufio.NewReader(os.Stdin),
	}
}

// ask reads one passphrase line. The prompt goes to stderr so stdout stays
// clean for scripts. The passphrase is never echoed or logged.
func (s passSource) ask(prompt string) (string, error) {
	if s.tty {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(os.Stdin.Fd())
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	line, err := s.in.ReadString('\n')
	if err != nil && !(err == io.EOF && line != "") {
		if err == io.EOF {
			return "", nil
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// passphrase reads the passphrase. When confirm is set and the input is a
// terminal, it is asked a second time and both must match.
func (s passSource) passphrase(confirm bool) (string, error) {
	pass, err := s.ask("口令: ")
	if err != nil {
		return "", err
	}
	if pass == "" {
		return "", errors.New("口令不能为空")
	}
	if confirm && s.tty {
		again, err := s.ask("再输一次: ")
		if err != nil {
			return "", err
		}
		if again != pass {
			return "", errors.New("两次输入的口令不一致，未生成备份")
		}
	}
	return pass, nil
}

// backupCmd: `magpie backup [--no-keys] [file]`
func backupCmd(args []string) error {
	noKeys := false
	file := ""
	for _, a := range args {
		switch {
		case a == "--no-keys":
			noKeys = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q\n\n%s", a, backupUsage)
		case file != "":
			return fmt.Errorf("too many arguments\n\n%s", backupUsage)
		default:
			file = a
		}
	}
	if file == "" {
		file = defaultBackupFile
	}

	// the passphrase comes first, so a refused passphrase leaves no file behind
	pass, err := newPassSource().passphrase(true)
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
		if providerHasKey(p) {
			withKey++
		}
	}
	fmt.Println(green.Render("✓"), "backup written to", bold.Render(file))
	fmt.Printf("  %d providers (%d with keys, %d without) · %d profiles · %d agent settings\n",
		len(b.Providers), withKey, len(b.Providers)-withKey, len(b.Profiles), len(b.Agents))
	fmt.Println(muted.Render("  subscriptions are not in it: sign in to them again on the other machine"))
	fmt.Println(muted.Render("  without the passphrase the file cannot be opened"))
	return nil
}

// restoreCmd: `magpie restore [--no-agents] <file>`
func restoreCmd(args []string) error {
	noAgents := false
	file := ""
	for _, a := range args {
		switch {
		case a == "--no-agents":
			noAgents = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q\n\n%s", a, backupUsage)
		case file != "":
			return fmt.Errorf("too many arguments\n\n%s", backupUsage)
		default:
			file = a
		}
	}
	if file == "" {
		return fmt.Errorf("missing backup file\n\n%s", backupUsage)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}

	// Ask for the passphrase only when the file is a backup this magpie can
	// read. An empty passphrase always fails the decryption, so a non-backup
	// or too-new file is reported here without a prompt.
	if _, err := backup.Open(data, ""); err != nil && !errors.Is(err, backup.ErrPassphrase) {
		return err
	}
	pass, err := newPassSource().passphrase(false)
	if err != nil {
		return err
	}
	b, err := backup.Open(data, pass)
	if errors.Is(err, backup.ErrPassphrase) {
		return errors.New("口令错误或文件被改动，本机配置未改动")
	}
	if err != nil {
		return err
	}

	parts := backup.All
	parts.Agents = !noAgents
	r, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}

	fmt.Println(green.Render("✓"), "restored from", bold.Render(file))
	fmt.Printf("  providers: %d added, %d replaced\n", r.Added, r.Replaced)
	if r.Settings {
		fmt.Println("  settings: restored")
	} else {
		fmt.Println("  settings: not in this backup, left as they are")
	}
	fmt.Printf("  profiles: %d restored\n", r.Profiles)
	if noAgents {
		fmt.Println("  agents: not changed (--no-agents)")
	} else {
		fmt.Printf("  agents: %d settings changed\n", r.Agents)
	}
	if len(r.NeedKey) > 0 {
		fmt.Println(amber.Render("!"), "these providers have no key yet:")
		for _, name := range r.NeedKey {
			fmt.Printf("    %s  · magpie provider key %s <key>\n", bold.Render(name), providerIDByName(b, name))
		}
	}
	if len(r.Skipped) > 0 {
		fmt.Println(amber.Render("!"), "skipped:")
		for _, s := range r.Skipped {
			fmt.Println("    " + s)
		}
	}
	return nil
}

// providerHasKey reports whether a provider carries any API key.
func providerHasKey(p provider.Provider) bool {
	if p.Key != "" {
		return true
	}
	for _, k := range p.Keys {
		if k.Key != "" {
			return true
		}
	}
	return false
}

// providerIDByName finds the id of the first provider in b with this name,
// or "<id>" when there is none.
func providerIDByName(b backup.Bundle, name string) string {
	for _, p := range b.Providers {
		if p.Name == name {
			return p.ID
		}
	}
	return "<id>"
}

// writeBackupFile replaces path with data atomically. The file is created
// readable and writable by its owner only, and the mode is set again on the
// final name so an existing file also ends up 0600.
func writeBackupFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".magpie-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // harmless once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
