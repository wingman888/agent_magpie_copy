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
)

const backupUsage = `usage:
  magpie backup [--no-keys] [file]      export the setup to one encrypted file (default magpie.magpie-backup)
  magpie restore [--no-agents] <file>   restore a file made by magpie backup`

// defaultBackupFile is the name `magpie backup` writes when none is given.
var defaultBackupFile = "magpie" + backup.Ext

type backupOpts struct {
	noKeys bool
	file   string
}

type restoreOpts struct {
	noAgents bool
	file     string
}

// parseBackupArgs reads the arguments after "backup".
func parseBackupArgs(args []string) (backupOpts, error) {
	o := backupOpts{file: defaultBackupFile}
	var pos []string
	for _, a := range args {
		switch {
		case a == "--no-keys":
			o.noKeys = true
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown option %q\n\n%s", a, backupUsage)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) > 1 {
		return o, fmt.Errorf("too many arguments\n\n%s", backupUsage)
	}
	if len(pos) == 1 {
		o.file = pos[0]
	}
	return o, nil
}

// parseRestoreArgs reads the arguments after "restore".
func parseRestoreArgs(args []string) (restoreOpts, error) {
	var o restoreOpts
	var pos []string
	for _, a := range args {
		switch {
		case a == "--no-agents":
			o.noAgents = true
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown option %q\n\n%s", a, backupUsage)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		return o, fmt.Errorf("restore needs exactly one backup file\n\n%s", backupUsage)
	}
	o.file = pos[0]
	return o, nil
}

// stdin is shared by every passphrase read, so that a line buffered by one
// read is not lost to the next.
var stdin *bufio.Reader

// stdinLine returns the next line of standard input without its line ending.
func stdinLine() (string, error) {
	if stdin == nil {
		stdin = bufio.NewReader(os.Stdin)
	}
	line, err := stdin.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func isTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// readSecret reads one line from the terminal with echo off. The prompt goes
// to stderr so that stdout stays clean.
func readSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	// Without echo control the line is still read, only shown as typed.
	restore, _ := disableEcho(os.Stdin)
	if restore != nil {
		defer restore()
	}
	line, err := stdinLine()
	fmt.Fprintln(os.Stderr)
	return line, err
}

// readPassphrase reads the passphrase of an existing backup: once, from the
// terminal without echo, or the first line of standard input.
func readPassphrase(prompt string) (string, error) {
	if !isTerminal() {
		return stdinLine()
	}
	return readSecret(prompt)
}

// newPassphrase reads the passphrase for a new backup. On a terminal it is
// asked twice and must match; from standard input the first line is used.
func newPassphrase() (string, error) {
	if !isTerminal() {
		p, err := stdinLine()
		if err != nil {
			return "", err
		}
		if p == "" {
			return "", backup.ErrEmptyPassphrase
		}
		return p, nil
	}
	p, err := readSecret("口令：")
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", backup.ErrEmptyPassphrase
	}
	again, err := readSecret("再输入一次：")
	if err != nil {
		return "", err
	}
	if again != p {
		return "", errors.New("两次输入的口令不一致，未生成备份")
	}
	return p, nil
}

// writeBackupFile writes data to path, readable and writable by its owner
// only. It is written beside the target and renamed, so a failure never
// leaves a half-written file behind.
func writeBackupFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".magpie-backup-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op once the file has been renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// backupCmd: `magpie backup [--no-keys] [file]`
func backupCmd(args []string) error {
	o, err := parseBackupArgs(args[1:])
	if err != nil {
		return err
	}
	pass, err := newPassphrase()
	if err != nil {
		return err
	}
	b, err := backup.Collect(!o.noKeys, version)
	if err != nil {
		return err
	}
	data, err := backup.Seal(b, pass)
	if err != nil {
		return err
	}
	if err := writeBackupFile(o.file, data); err != nil {
		return err
	}

	keyed := 0
	for _, p := range b.Providers {
		if p.Key != "" || len(p.Keys) > 0 {
			keyed++
		}
	}
	fmt.Println(green.Render("✓"), "backed up to", bold.Render(o.file))
	fmt.Printf("  提供商 %d 个（含 Key %d 个，不含 Key %d 个）· 方案 %d 个 · Agent 设置 %d 项\n",
		len(b.Providers), keyed, len(b.Providers)-keyed, len(b.Profiles), len(b.Agents))
	fmt.Println(muted.Render("  订阅账号不在备份中，在另一台机器上需要重新登录。"))
	fmt.Println(muted.Render("  口令丢失则无法打开此文件。"))
	return nil
}

// restoreCmd: `magpie restore [--no-agents] <file>`
func restoreCmd(args []string) error {
	o, err := parseRestoreArgs(args[1:])
	if err != nil {
		return err
	}
	data, err := os.ReadFile(o.file)
	if err != nil {
		return err
	}
	// Learn what the file is before asking for a passphrase: an empty
	// passphrase is refused only for a real backup, so other files still
	// get their own message.
	if _, err := backup.Open(data, ""); err != nil && !errors.Is(err, backup.ErrEmptyPassphrase) {
		return err
	}
	pass, err := readPassphrase("口令：")
	if err != nil {
		return err
	}
	if pass == "" {
		return backup.ErrEmptyPassphrase
	}
	b, err := backup.Open(data, pass)
	if errors.Is(err, backup.ErrPassphrase) {
		return errors.New("口令错误或文件已被改动（本机配置未做任何更改）")
	}
	if err != nil {
		return err
	}

	parts := backup.All
	parts.Agents = !o.noAgents
	r, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}

	fmt.Println(green.Render("✓"), "restored", bold.Render(o.file))
	fmt.Printf("  提供商：新增 %d 个，替换 %d 个\n", r.Added, r.Replaced)
	if r.Settings {
		fmt.Println("  设置：已恢复")
	} else {
		fmt.Println("  设置：备份中没有，未改动")
	}
	fmt.Printf("  方案：恢复 %d 个\n", r.Profiles)
	if o.noAgents {
		fmt.Println("  Agent 设置：未恢复（--no-agents）")
	} else {
		fmt.Printf("  Agent 设置：变更 %d 项\n", r.Agents)
	}

	if len(r.NeedKey) > 0 {
		idByName := map[string]string{}
		for _, p := range b.Providers {
			name := p.Name
			if name == "" {
				name = p.ID
			}
			if _, ok := idByName[name]; !ok {
				idByName[name] = strings.ToLower(strings.TrimSpace(p.ID))
			}
		}
		fmt.Println(amber.Render("!"), "以下提供商还没有 Key：")
		for _, name := range r.NeedKey {
			if id := idByName[name]; id != "" {
				fmt.Printf("  · %s (%s)\n", name, id)
			} else {
				fmt.Printf("  · %s\n", name)
			}
		}
		fmt.Println(muted.Render("  用 magpie provider key <id> <key> 补充。"))
	}
	if len(r.Skipped) > 0 {
		fmt.Println(amber.Render("!"), "以下 Agent 设置被跳过：")
		for _, s := range r.Skipped {
			fmt.Printf("  · %s\n", s)
		}
	}
	return nil
}
