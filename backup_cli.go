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
  magpie backup [--no-keys] [file]     save every provider, icon, setting, profile and agent model to one
                                       passphrase-protected file (default magpie` + backup.Ext + `)
                                       --no-keys leaves out API keys and credential-like request headers
  magpie restore [--no-agents] <file>  bring such a file back; --no-agents leaves the agents' models alone

  The passphrase is asked for on a terminal, or read from the first line of stdin:
    echo "passphrase" | magpie backup`

// stdin and stdinTTY are swapped in tests.
var (
	stdin    io.Reader = os.Stdin
	stdinTTY           = func() bool { return term.IsTerminal(os.Stdin.Fd()) }
)

// backupCmd: `magpie backup [--no-keys] [file]`
func backupCmd(args []string) error {
	keys := true
	var file string
	for _, a := range args {
		switch {
		case a == "--no-keys":
			keys = false
		case a == "-h" || a == "--help":
			fmt.Println(backupUsage)
			return nil
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q\n\n%s", a, backupUsage)
		case file != "":
			return fmt.Errorf("one file at most\n\n%s", backupUsage)
		default:
			file = a
		}
	}
	if file == "" {
		file = "magpie" + backup.Ext
	}
	pass, err := readPassphrase(true)
	if err != nil {
		return err
	}
	b, err := backup.Collect(keys, "magpie "+version)
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
	withKeys := "with keys"
	if !keys {
		withKeys = "no keys"
	}
	fmt.Println(green.Render("✓"), "saved", bold.Render(file), muted.Render("·"),
		plural(len(b.Providers), "provider")+" ("+withKeys+")", muted.Render("·"),
		plural(len(b.Profiles), "profile"), muted.Render("·"),
		plural(len(b.Agents), "agent setting"))
	fmt.Println(muted.Render("  subscriptions are not in it: sign in to them on the other machine (订阅不在备份中,需在另一台机器登录)"))
	fmt.Println(muted.Render("  keep the passphrase: without it the file can't be opened (口令丢失则文件无法打开)"))
	return nil
}

// restoreCmd: `magpie restore [--no-agents] <file>`
func restoreCmd(args []string) error {
	parts := backup.All
	var file string
	for _, a := range args {
		switch {
		case a == "--no-agents":
			parts.Agents = false
		case a == "-h" || a == "--help":
			fmt.Println(backupUsage)
			return nil
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q\n\n%s", a, backupUsage)
		case file != "":
			return fmt.Errorf("one file at most\n\n%s", backupUsage)
		default:
			file = a
		}
	}
	if file == "" {
		return fmt.Errorf("which file?\n\n%s", backupUsage)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if err := backup.Check(data); err != nil {
		return restoreError(file, err)
	}
	pass, err := readPassphrase(false)
	if err != nil {
		return err
	}
	b, err := backup.Open(data, pass)
	if err != nil {
		return restoreError(file, err)
	}
	r, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}
	settingsNote := "settings unchanged"
	if r.Settings {
		settingsNote = "settings restored"
	}
	fmt.Println(green.Render("✓"), "restored", bold.Render(file), muted.Render("·"),
		fmt.Sprintf("providers: %d added, %d replaced", r.Added, r.Replaced), muted.Render("·"),
		settingsNote, muted.Render("·"),
		plural(r.Profiles, "profile"), muted.Render("·"),
		plural(r.Agents, "agent setting")+" changed")
	if len(r.NeedKey) > 0 {
		var names []string
		for _, n := range r.NeedKey {
			if p, err := provider.Find(n); err == nil {
				n += " (" + p.ID + ")"
			}
			names = append(names, n)
		}
		fmt.Println(amber.Render("!"), "no key yet:", strings.Join(names, ", "),
			muted.Render("· add one with magpie provider key <id> <key>"))
	}
	if len(r.Skipped) > 0 {
		fmt.Println(amber.Render("!"), "skipped",
			muted.Render("(the agent isn't installed here, or its model isn't available for now):"))
		for _, s := range r.Skipped {
			fmt.Println("   ", s)
		}
	}
	if !parts.Agents {
		fmt.Println(muted.Render("  agents left as they were (--no-agents)"))
	} else if r.Agents > 0 {
		fmt.Println(muted.Render("  agents read their config at start: running sessions see it after a restart"))
	}
	fmt.Println(muted.Render("  subscriptions don't come along: sign in to them on this machine"))
	return nil
}

func restoreError(file string, err error) error {
	switch {
	case errors.Is(err, backup.ErrNotBackup):
		return fmt.Errorf("%s: 不是 magpie 备份 — not a magpie backup", file)
	case errors.Is(err, backup.ErrTooNew):
		return fmt.Errorf("%s was made by a newer magpie: update magpie to restore it (需升级 magpie: magpie update)", file)
	case errors.Is(err, backup.ErrPassphrase):
		return fmt.Errorf("%s: wrong passphrase, or the file was modified (口令错误或文件被改动); nothing was changed", file)
	}
	return err
}

// readPassphrase asks for the passphrase without echo on a terminal, twice
// when confirm is set; otherwise it is the first line of stdin.
func readPassphrase(confirm bool) (string, error) {
	if !stdinTTY() {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		pass := strings.TrimRight(line, "\r\n")
		if pass == "" {
			return "", errors.New("the passphrase is empty (give it on the first line of stdin)")
		}
		return pass, nil
	}
	ask := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(os.Stdin.Fd())
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	pass, err := ask("Passphrase: ")
	if err != nil {
		return "", err
	}
	if pass == "" {
		return "", errors.New("the passphrase is empty")
	}
	if confirm {
		again, err := ask("Again: ")
		if err != nil {
			return "", err
		}
		if again != pass {
			return "", errors.New("the passphrases don't match; nothing was written")
		}
	}
	return pass, nil
}

// writePrivate writes a file only its owner can read: a temp file beside
// it, renamed over it once complete.
func writePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
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
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
