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
	"github.com/yetone/magpie/internal/provider"
)

const (
	defaultBackupFile = "magpie" + backup.Ext

	backupUsage  = "usage: magpie backup [--no-keys] [file]"
	restoreUsage = "usage: magpie restore [--no-agents] <file>"
)

// parseBackupArgs reads the arguments after `magpie backup`.
func parseBackupArgs(args []string) (noKeys bool, file string, err error) {
	for _, a := range args {
		switch {
		case a == "--no-keys":
			noKeys = true
		case strings.HasPrefix(a, "-"):
			return false, "", fmt.Errorf("unknown option %q\n%s", a, backupUsage)
		case file != "":
			return false, "", fmt.Errorf("too many arguments\n%s", backupUsage)
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
			return false, "", fmt.Errorf("unknown option %q\n%s", a, restoreUsage)
		case file != "":
			return false, "", fmt.Errorf("too many arguments\n%s", restoreUsage)
		default:
			file = a
		}
	}
	if file == "" {
		return false, "", fmt.Errorf("missing backup file\n%s", restoreUsage)
	}
	return noAgents, file, nil
}

// readPassphrase reads the passphrase from in. On a terminal it is not echoed,
// and with confirm it is asked twice. Otherwise the first line of in is the
// passphrase. An empty passphrase is an error. The passphrase is never printed.
func readPassphrase(in *os.File, out io.Writer, confirm bool) (string, error) {
	if term.IsTerminal(in.Fd()) {
		fmt.Fprint(out, "口令：")
		first, err := term.ReadPassword(in.Fd())
		fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("读取口令失败: %w", err)
		}
		if len(first) == 0 {
			return "", errors.New("口令不能为空")
		}
		if confirm {
			fmt.Fprint(out, "再输入一次：")
			second, err := term.ReadPassword(in.Fd())
			fmt.Fprintln(out)
			if err != nil {
				return "", fmt.Errorf("读取口令失败: %w", err)
			}
			if string(first) != string(second) {
				return "", errors.New("两次输入的口令不一致，未生成备份")
			}
		}
		return string(first), nil
	}

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("读取口令失败: %w", err)
	}
	pass := strings.TrimRight(line, "\r\n")
	if pass == "" {
		return "", errors.New("口令不能为空")
	}
	return pass, nil
}

// backupCmd: `magpie backup [--no-keys] [file]`
func backupCmd(args []string) error {
	noKeys, file, err := parseBackupArgs(args[1:])
	if err != nil {
		return err
	}
	pass, err := readPassphrase(os.Stdin, os.Stderr, true)
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
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return err
	}
	// WriteFile keeps the mode of an existing file; make sure it is private.
	if err := os.Chmod(file, 0o600); err != nil {
		return err
	}

	keys := "含 Key"
	if noKeys {
		keys = "不含 Key"
	}
	fmt.Println(green.Render("✓"), "backed up to", bold.Render(file), muted.Render(
		fmt.Sprintf("· %d 个提供商（%s）· %d 个方案 · %d 项 Agent 设置", len(b.Providers), keys, len(b.Profiles), len(b.Agents))))
	fmt.Println(muted.Render("  订阅账号不在备份中，需在另一台机器上自行登录"))
	fmt.Println(muted.Render("  口令丢失则该文件无法打开"))
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
		return err
	}
	pass, err := readPassphrase(os.Stdin, os.Stderr, false)
	if err != nil {
		return err
	}
	b, err := backup.Open(data, pass)
	if errors.Is(err, backup.ErrPassphrase) {
		return errors.New("口令错误或备份文件已被改动，本机配置未做任何更改")
	}
	if err != nil {
		// not a backup, or made by a newer magpie: the error says which
		return err
	}

	parts := backup.All
	if noAgents {
		parts = backup.Parts{Providers: true, Settings: true, Profiles: true, Agents: false}
	}
	r, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}

	fmt.Println(green.Render("✓"), "providers:", fmt.Sprintf("%d added, %d replaced", r.Added, r.Replaced))
	if r.Settings {
		fmt.Println(green.Render("✓"), "settings restored")
	} else {
		fmt.Println(muted.Render("  settings: not in the backup, left as they are"))
	}
	fmt.Println(green.Render("✓"), fmt.Sprintf("profiles: %d restored", r.Profiles))
	if noAgents {
		fmt.Println(muted.Render("  agents: not restored (--no-agents)"))
	} else {
		fmt.Println(green.Render("✓"), fmt.Sprintf("agents: %d changed", r.Agents))
	}

	if len(r.NeedKey) > 0 {
		fmt.Println(amber.Render("!"), "these providers have no key yet:")
		for _, name := range r.NeedKey {
			fmt.Println("  ", name, muted.Render("· magpie provider key "+providerIDFor(name)+" <key>"))
		}
	}
	if len(r.Skipped) > 0 {
		fmt.Println(amber.Render("!"), "skipped:")
		for _, s := range r.Skipped {
			fmt.Println("  ", s)
		}
	}
	return nil
}

// providerIDFor finds the id of the provider called name, for the hint to add
// its key. It falls back to a placeholder when no provider has that name.
func providerIDFor(name string) string {
	for _, p := range provider.All() {
		if p.Name == name {
			return p.ID
		}
	}
	return "<id>"
}
