package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/edit"
)

// backupCmd writes every provider, setting, profile and agent model to one
// file sealed with a passphrase: magpie backup [--no-keys] [file].
func backupCmd(args []string) error {
	keys, file := true, ""
	for _, a := range args {
		switch {
		case a == "--no-keys":
			keys = false
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s (magpie backup [--no-keys] [file])", a)
		case file == "":
			file = a
		default:
			return errors.New("usage: magpie backup [--no-keys] [file]")
		}
	}
	if file == "" {
		file = "magpie" + backup.Ext
	}
	b, err := backup.Collect(keys, version)
	if err != nil {
		return err
	}
	pass, err := passphrase("Passphrase for the backup: ", true)
	if err != nil {
		return err
	}
	data, err := backup.Seal(b, pass)
	if err != nil {
		return err
	}
	if err := edit.WriteAtomic(file, data); err != nil {
		return err
	}
	os.Chmod(file, 0o600)
	what := "with their keys"
	if !keys {
		what = "without keys"
	}
	fmt.Printf("%s %s: %d providers %s, %d profiles, %d agent settings\n",
		green.Render("saved"), file, len(b.Providers), what, len(b.Profiles), len(b.Agents))
	fmt.Println(muted.Render("Subscriptions are not in it: sign in to them on the other machine. Keep the passphrase: without it the file can't be opened."))
	return nil
}

// restoreCmd puts a backup in: magpie restore [--no-agents] <file>.
func restoreCmd(args []string) error {
	parts, file := backup.All, ""
	for _, a := range args {
		switch {
		case a == "--no-agents":
			parts.Agents = false
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s (magpie restore [--no-agents] <file>)", a)
		case file == "":
			file = a
		default:
			return errors.New("usage: magpie restore [--no-agents] <file>")
		}
	}
	if file == "" {
		return errors.New("usage: magpie restore [--no-agents] <file>")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	pass, err := passphrase("Passphrase: ", false)
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
	fmt.Printf("%s providers: %d added, %d replaced", green.Render("restored"), r.Added, r.Replaced)
	if r.Settings {
		fmt.Print("; settings")
	}
	fmt.Printf("; %d profiles; %d agent settings changed\n", r.Profiles, r.Agents)
	if len(r.NeedKey) > 0 {
		fmt.Println(muted.Render("Needs a key (magpie provider key <id>): " + strings.Join(r.NeedKey, ", ")))
	}
	if len(r.Skipped) > 0 {
		fmt.Println(muted.Render("Left as they are (agent not here, or its model can't be reached yet): " + strings.Join(r.Skipped, ", ")))
	}
	return nil
}

// passphrase reads one from the terminal without echo, asked twice when
// it is being set; piped in, it is the first line of stdin.
func passphrase(prompt string, confirm bool) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if err != nil {
				return "", fmt.Errorf("no passphrase on stdin: %w", err)
			}
			return "", errors.New("the passphrase is empty")
		}
		return line, nil
	}
	read := func(p string) (string, error) {
		fmt.Fprint(os.Stderr, p)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	pass, err := read(prompt)
	if err != nil {
		return "", err
	}
	if pass == "" {
		return "", errors.New("the passphrase is empty")
	}
	if confirm {
		again, err := read("Again: ")
		if err != nil {
			return "", err
		}
		if again != pass {
			return "", errors.New("the passphrases differ")
		}
	}
	return pass, nil
}
