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
	defaultBackupFile = "magpie" + backup.Ext
	backupUsage       = "usage: magpie backup [--no-keys] [file]"
	restoreUsage      = "usage: magpie restore [--no-agents] <file>"
	// probePassphrase is used only to learn whether a file is a backup at
	// all, and of which version, before anyone is asked for a passphrase.
	// Open never accepts it, so the result is always ErrPassphrase or a
	// format error.
	probePassphrase = "magpie-probe"
)

var (
	errEmptyPassphrase = errors.New("口令不能为空")
	errPassMismatch    = errors.New("两次输入的口令不一致")
	errWrongPassphrase = errors.New("口令错误或文件被改动，本机配置未做任何改动")
)

// backupCmd implements `magpie backup [--no-keys] [file]`.
func backupCmd(args []string) error {
	noKeys, file, err := parseBackupArgs(args[1:])
	if err != nil {
		return err
	}
	// The passphrase comes first: an empty or mismatched one must fail
	// before anything is collected or written.
	pass, err := readPassphrase(os.Stdin, true)
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
		return fmt.Errorf("could not write %s: %w", file, err)
	}

	withKey, withoutKey := 0, 0
	for _, p := range b.Providers {
		if p.Key != "" || len(p.Keys) > 0 {
			withKey++
		} else {
			withoutKey++
		}
	}
	settingsNote := "设置 无"
	if b.Settings != nil {
		settingsNote = "设置 有"
	}
	fmt.Println(green.Render("✓"), "已备份到", bold.Render(file))
	fmt.Println("  "+fmt.Sprintf("提供商 %d 个（含 Key %d 个 / 不含 Key %d 个）· 方案 %d 个 · Agent 设置 %d 个 · %s",
		len(b.Providers), withKey, withoutKey, len(b.Profiles), len(b.Agents), settingsNote))
	fmt.Println(muted.Render("  订阅账号不在备份中，需在另一台机器上分别登录。"))
	fmt.Println(muted.Render("  口令丢失则无法打开此备份文件。"))
	return nil
}

// restoreCmd implements `magpie restore [--no-agents] <file>`.
func restoreCmd(args []string) error {
	noAgents, file, err := parseRestoreArgs(args[1:])
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("could not read %s: %w", file, err)
	}
	// Refuse non-backups and newer backups before asking for a passphrase.
	if _, err := backup.Open(data, probePassphrase); err != nil && !errors.Is(err, backup.ErrPassphrase) {
		return err
	}
	pass, err := readPassphrase(os.Stdin, false)
	if err != nil {
		return err
	}
	b, err := backup.Open(data, pass)
	if errors.Is(err, backup.ErrPassphrase) {
		return errWrongPassphrase
	}
	if err != nil {
		return err
	}

	parts := backup.All
	if noAgents {
		parts.Agents = false
	}
	res, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}

	fmt.Println(green.Render("✓"), "已从", bold.Render(file), "恢复")
	fmt.Printf("  提供商：新增 %d 个，替换 %d 个\n", res.Added, res.Replaced)
	switch {
	case res.Settings:
		fmt.Println("  设置：已恢复")
	case b.Settings == nil:
		fmt.Println(muted.Render("  设置：备份中没有"))
	default:
		fmt.Println(muted.Render("  设置：未恢复"))
	}
	fmt.Printf("  方案：%d 个\n", res.Profiles)
	if parts.Agents {
		fmt.Printf("  Agent 设置：变更 %d 项\n", res.Agents)
		if res.Agents > 0 {
			fmt.Println(muted.Render("  已在运行的 Agent 会话需重启后才会使用新模型。"))
		}
	} else {
		fmt.Println(muted.Render("  Agent 设置：未恢复（--no-agents）"))
	}

	if len(res.NeedKey) > 0 {
		ids := providerIDsByName(b)
		fmt.Println(bold.Render("需要补充 Key 的提供商："))
		for _, name := range res.NeedKey {
			if id, ok := ids[name]; ok {
				fmt.Printf("  %s  →  magpie provider key %s\n", name, id)
			} else {
				fmt.Printf("  %s\n", name)
			}
		}
	}
	if len(res.Skipped) > 0 {
		fmt.Println(bold.Render("已跳过："))
		for _, s := range res.Skipped {
			fmt.Println("  · " + s)
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
	switch len(pos) {
	case 0:
		return false, "", fmt.Errorf("missing backup file\n%s", restoreUsage)
	case 1:
		return noAgents, pos[0], nil
	}
	return false, "", fmt.Errorf("too many arguments\n%s", restoreUsage)
}

// readPassphrase reads the passphrase. On a terminal it is typed without
// echo, twice when confirm is set. Otherwise the first line of in is used.
func readPassphrase(in *os.File, confirm bool) (string, error) {
	if !isTerminal(in) {
		return readFirstLine(in)
	}
	pass, err := promptSecret(in, "口令：")
	if err != nil {
		return "", err
	}
	if pass == "" {
		return "", errEmptyPassphrase
	}
	if confirm {
		again, err := promptSecret(in, "再输入一次：")
		if err != nil {
			return "", err
		}
		if again != pass {
			return "", errPassMismatch
		}
	}
	return pass, nil
}

// promptSecret shows a prompt on stderr and reads one line from the terminal
// with echo switched off.
func promptSecret(in *os.File, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	restore, ok := echoOff(in)
	line, err := bufio.NewReader(in).ReadString('\n')
	if ok {
		restore()
	}
	fmt.Fprintln(os.Stderr)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readFirstLine takes the first line of a non-terminal input as the
// passphrase. Nothing is asked again and nothing is echoed.
func readFirstLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errEmptyPassphrase
	}
	return line, nil
}

// writeBackupFile writes data to path readable and writable by this user only.
// It writes a temporary file beside path and renames it into place.
func writeBackupFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".magpie-backup-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // harmless once renamed
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
	return os.Rename(tmpName, path)
}

// providerIDsByName maps each provider's display name to its id, as it is
// in the backup.
func providerIDsByName(b backup.Bundle) map[string]string {
	ids := map[string]string{}
	for _, p := range b.Providers {
		name := providerName(p)
		if _, ok := ids[name]; !ok {
			ids[name] = p.ID
		}
	}
	return ids
}

func providerName(p provider.Provider) string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}
