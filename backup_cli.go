package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

const (
	backupUsage  = "usage: magpie backup [--no-keys] [file]     (default file: magpie.magpie-backup)"
	restoreUsage = "usage: magpie restore [--no-agents] <file>"

	defaultBackupFile = "magpie" + backup.Ext
)

// backupCmd: `magpie backup [--no-keys] [file]` — every piece of magpie's
// configuration, sealed in one file under a passphrase.
func backupCmd(args []string) error {
	noKeys, file, err := parseBackupArgs(args[1:])
	if err != nil {
		return err
	}
	b, err := backup.Collect(!noKeys, version)
	if err != nil {
		return err
	}
	pass, err := readPassphrase(os.Stdin, "Passphrase: ", true)
	if err != nil {
		return err
	}
	if pass == "" {
		return errors.New("口令为空，未写入备份")
	}
	data, err := backup.Seal(b, pass)
	pass = ""
	if err != nil {
		return err
	}
	if err := writeBackup(file, data); err != nil {
		return err
	}

	withKeys := 0
	for _, p := range b.Providers {
		if providerHasKey(p) {
			withKeys++
		}
	}
	fmt.Println(green.Render("✓"), "backed up to", bold.Render(file),
		muted.Render(fmt.Sprintf("· %d providers (%d with keys, %d without) · %d profiles · %d agent settings",
			len(b.Providers), withKeys, len(b.Providers)-withKeys, len(b.Profiles), len(b.Agents))))
	fmt.Println(faint.Render("  subscriptions are not included: sign in on the other machine. 请牢记口令：忘记口令则无法打开该文件。"))
	return nil
}

// restoreCmd: `magpie restore [--no-agents] <file>` — puts a backup made by
// `magpie backup` back into this machine.
func restoreCmd(args []string) error {
	noAgents, file, err := parseRestoreArgs(args[1:])
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	// Preflight without a passphrase: a file that is not a backup, or one from
	// a newer magpie, fails here and never asks for a passphrase.
	if _, err := backup.Open(data, ""); err != nil && !errors.Is(err, backup.ErrPassphrase) {
		return err
	}
	pass, err := readPassphrase(os.Stdin, "Passphrase: ", false)
	if err != nil {
		return err
	}
	if pass == "" {
		return errors.New("口令为空，本机配置未做任何改动")
	}
	b, err := backup.Open(data, pass)
	pass = ""
	if err != nil {
		if errors.Is(err, backup.ErrPassphrase) {
			return errors.New("口令错误或文件被改动，本机配置未做任何改动")
		}
		return err
	}

	parts := backup.All
	parts.Agents = !noAgents
	res, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}

	fmt.Println(green.Render("✓"), "restored",
		muted.Render(fmt.Sprintf("· providers: %d added, %d replaced", res.Added, res.Replaced)))
	settingsState := "not in the backup"
	if res.Settings {
		settingsState = "restored"
	}
	agentsState := fmt.Sprintf("%d changed", res.Agents)
	if noAgents {
		agentsState = "skipped (--no-agents)"
	}
	fmt.Printf("  settings: %s · profiles: %d · agent settings: %s\n", settingsState, res.Profiles, agentsState)

	if len(res.NeedKey) > 0 {
		fmt.Println(bold.Render("! no API key yet for:"), strings.Join(res.NeedKey, ", "))
		for _, name := range res.NeedKey {
			fmt.Printf("  magpie provider key %s <key>\n", providerIDForName(name))
		}
	}
	if len(res.Skipped) > 0 {
		fmt.Println(bold.Render("! skipped:"))
		for _, s := range res.Skipped {
			fmt.Println("  " + s)
		}
	}
	return nil
}

// parseBackupArgs reads the arguments after "backup".
func parseBackupArgs(args []string) (noKeys bool, file string, err error) {
	var pos []string
	for _, a := range args {
		switch {
		case a == "--no-keys":
			noKeys = true
		case strings.HasPrefix(a, "-"):
			return false, "", fmt.Errorf("unknown option %q\n%s", a, backupUsage)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) > 1 {
		return false, "", fmt.Errorf("too many arguments\n%s", backupUsage)
	}
	file = defaultBackupFile
	if len(pos) == 1 {
		if pos[0] == "" {
			return false, "", fmt.Errorf("empty file name\n%s", backupUsage)
		}
		file = pos[0]
	}
	return noKeys, file, nil
}

// parseRestoreArgs reads the arguments after "restore".
func parseRestoreArgs(args []string) (noAgents bool, file string, err error) {
	var pos []string
	for _, a := range args {
		switch {
		case a == "--no-agents":
			noAgents = true
		case strings.HasPrefix(a, "-"):
			return false, "", fmt.Errorf("unknown option %q\n%s", a, restoreUsage)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 || pos[0] == "" {
		return false, "", fmt.Errorf("%s", restoreUsage)
	}
	return noAgents, pos[0], nil
}

// readPassphrase takes the passphrase from a terminal (typed without echo,
// twice when confirm is set) or, from anywhere else, the first line of input.
// The passphrase is never printed.
func readPassphrase(in *os.File, prompt string, confirm bool) (string, error) {
	if !isTerminal(in) {
		return readLine(in)
	}
	pass, err := readHidden(in, prompt)
	if err != nil {
		return "", err
	}
	if confirm {
		again, err := readHidden(in, "Repeat passphrase: ")
		if err != nil {
			return "", err
		}
		if again != pass {
			return "", errors.New("两次口令不一致，未写入备份")
		}
	}
	return pass, nil
}

// readLine returns the first line of r without its line break.
func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// writeBackup replaces path with data atomically: the file is written beside
// it, set to 0600, and renamed into place, so a failure leaves no partial file.
func writeBackup(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".magpie-backup-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func providerHasKey(p provider.Provider) bool {
	return strings.TrimSpace(p.Key) != "" || len(p.Keys) > 0
}

// providerIDForName maps a provider's name, as the restore reports it, back to
// its id for the `magpie provider key` hint.
func providerIDForName(name string) string {
	for _, p := range provider.StoredProviders() {
		if strings.TrimSpace(p.Name) == name {
			return p.ID
		}
	}
	return name
}
