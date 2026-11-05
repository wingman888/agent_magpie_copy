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
	backupUsage       = "用法：magpie backup [--no-keys] [文件]"
	restoreUsage      = "用法：magpie restore [--no-agents] <文件>"

	backupHelp = backupUsage + `

把 magpie 的全部配置加密保存为一个文件：提供商（含 Key、请求头）、提供商图标、
设置、全部方案、各 Agent 当前的模型。订阅账号不在其中，需在另一台机器上登录。

  --no-keys   文件中不含任何 API Key，也不含凭据类请求头（如 X-Api-Key）
  文件        默认为 ./magpie.magpie-backup

口令在终端中输入两次且须一致；通过管道提供时取标准输入的第一行。口令丢失则备份无法打开。
`
	restoreHelp = restoreUsage + `

从 magpie backup 生成的文件恢复配置。提供商按 id 匹配：本机已有的替换（保留本机的 Key），
没有的新增。设置整体覆盖，方案按名称写入。

  --no-agents   不恢复各 Agent 的模型选择，其余照常恢复

口令在终端中输入一次；通过管道提供时取标准输入的第一行。
`
)

var errEmptyPass = errors.New("口令不能为空")

// splitArgs separates flags from positional arguments. known maps each
// accepted flag to the variable it switches on. "--" ends the flags.
func splitArgs(args []string, usage string, known map[string]*bool) (pos []string, help bool, err error) {
	flags := true
	for _, s := range args {
		switch {
		case flags && s == "--":
			flags = false
		case flags && (s == "-h" || s == "--help"):
			help = true
		case flags && strings.HasPrefix(s, "-"):
			on, ok := known[s]
			if !ok {
				return nil, false, fmt.Errorf("未知参数 %s\n%s", s, usage)
			}
			*on = true
		default:
			pos = append(pos, s)
		}
	}
	return pos, help, nil
}

type backupArgs struct {
	noKeys bool
	file   string
	help   bool
}

func parseBackupArgs(args []string) (backupArgs, error) {
	var a backupArgs
	pos, help, err := splitArgs(args, backupUsage, map[string]*bool{"--no-keys": &a.noKeys})
	if err != nil {
		return a, err
	}
	if help {
		a.help = true
		return a, nil
	}
	if len(pos) > 1 {
		return a, fmt.Errorf("多余的参数 %s\n%s", pos[1], backupUsage)
	}
	a.file = defaultBackupFile
	if len(pos) == 1 {
		if pos[0] == "" {
			return a, fmt.Errorf("文件名不能为空\n%s", backupUsage)
		}
		a.file = pos[0]
	}
	return a, nil
}

type restoreArgs struct {
	noAgents bool
	file     string
	help     bool
}

func parseRestoreArgs(args []string) (restoreArgs, error) {
	var a restoreArgs
	pos, help, err := splitArgs(args, restoreUsage, map[string]*bool{"--no-agents": &a.noAgents})
	if err != nil {
		return a, err
	}
	if help {
		a.help = true
		return a, nil
	}
	if len(pos) != 1 || pos[0] == "" {
		if len(pos) > 1 {
			return a, fmt.Errorf("多余的参数 %s\n%s", pos[1], restoreUsage)
		}
		return a, fmt.Errorf("缺少备份文件\n%s", restoreUsage)
	}
	a.file = pos[0]
	return a, nil
}

// isTerminal reports whether f is a character device, as a terminal is.
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// readLine returns the first line of r without its line ending.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// passphrase asks for the passphrase. On a terminal it is read with echo
// off, twice when confirm is set. Otherwise the first line of in is used.
// The passphrase is never printed.
func passphrase(in *os.File, confirm bool) (string, error) {
	if !isTerminal(in) {
		p, err := readLine(bufio.NewReader(in))
		if err != nil {
			return "", err
		}
		if p == "" {
			return "", errEmptyPass
		}
		return p, nil
	}
	fmt.Fprint(os.Stderr, "口令：")
	p, err := readSecret(in)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", errEmptyPass
	}
	if confirm {
		fmt.Fprint(os.Stderr, "再输入一次：")
		again, err := readSecret(in)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if again != p {
			return "", errors.New("两次输入的口令不一致")
		}
	}
	return p, nil
}

// writeSecretFile replaces path with data, readable and writable by its
// owner only. The data goes to a temporary file first, so a failed write
// leaves any earlier backup in place.
func writeSecretFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".magpie-backup-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func backupCmd(args []string) error {
	a, err := parseBackupArgs(args)
	if err != nil {
		return err
	}
	if a.help {
		fmt.Print(backupHelp)
		return nil
	}
	pass, err := passphrase(os.Stdin, true)
	if err != nil {
		return err
	}
	b, err := backup.Collect(!a.noKeys, version)
	if err != nil {
		return err
	}
	data, err := backup.Seal(b, pass)
	if err != nil {
		return err
	}
	if err := writeSecretFile(a.file, data); err != nil {
		return err
	}
	printBackupSummary(a.file, b)
	return nil
}

func printBackupSummary(file string, b backup.Bundle) {
	withKey := 0
	for _, p := range b.Providers {
		if p.Key != "" || len(p.Keys) > 0 {
			withKey++
		}
	}
	total := len(b.Providers)
	fmt.Println(green.Render("✓"), "已备份到", bold.Render(file))
	fmt.Printf("  提供商 %d 个（含 Key %d 个，不含 Key %d 个）\n", total, withKey, total-withKey)
	fmt.Printf("  方案 %d 个\n", len(b.Profiles))
	fmt.Printf("  Agent 设置 %d 项\n", len(b.Agents))
	fmt.Println(muted.Render("  订阅账号不在备份中，需在另一台机器上分别登录。"))
	fmt.Println(muted.Render("  口令丢失则备份文件无法打开。"))
}

func restoreCmd(args []string) error {
	a, err := parseRestoreArgs(args)
	if err != nil {
		return err
	}
	if a.help {
		fmt.Print(restoreHelp)
		return nil
	}
	data, err := os.ReadFile(a.file)
	if err != nil {
		return fmt.Errorf("读取备份失败：%w", err)
	}
	// An empty passphrase makes Open check the format and version and stop
	// before any decryption. A file that is not a backup, or that a newer
	// magpie wrote, is reported here without asking for a passphrase.
	if _, err := backup.Open(data, ""); err != nil && !errors.Is(err, backup.ErrPassphrase) {
		return openError(err)
	}
	pass, err := passphrase(os.Stdin, false)
	if err != nil {
		return err
	}
	parts := backup.All
	parts.Agents = !a.noAgents
	res, err := restoreData(data, pass, parts)
	if err != nil {
		return err
	}
	printRestoreSummary(res, a.noAgents)
	return nil
}

// restoreData opens the backup and then applies the chosen parts. Opening
// comes first, so a wrong passphrase or a changed file changes nothing here.
func restoreData(data []byte, pass string, parts backup.Parts) (backup.Result, error) {
	b, err := backup.Open(data, pass)
	if err != nil {
		return backup.Result{}, openError(err)
	}
	return backup.Restore(b, parts)
}

// openError turns a backup.Open failure into a message for the user.
func openError(err error) error {
	switch {
	case errors.Is(err, backup.ErrPassphrase):
		return errors.New("口令错误或文件已被改动")
	case errors.Is(err, backup.ErrNotBackup):
		return errors.New("不是 magpie 备份文件")
	case errors.Is(err, backup.ErrNewer):
		return errors.New("这个备份由更新版本的 magpie 生成，请先升级 magpie")
	}
	return fmt.Errorf("无法读取备份：%w", err)
}

func printRestoreSummary(res backup.Result, noAgents bool) {
	fmt.Println(green.Render("✓"), "已从备份恢复")
	fmt.Printf("  提供商：新增 %d 个，替换 %d 个\n", res.Added, res.Replaced)
	if res.Settings {
		fmt.Println("  设置：已恢复")
	} else {
		fmt.Println("  设置：备份中没有设置，未改动")
	}
	fmt.Printf("  方案：%d 个\n", res.Profiles)
	if noAgents {
		fmt.Println("  Agent 设置：未恢复（--no-agents）")
	} else {
		fmt.Printf("  Agent 设置：变更 %d 项\n", res.Agents)
	}
	if len(res.NeedKey) > 0 {
		fmt.Println(amber.Render("!"), "以下提供商还没有 Key，请补充：")
		for _, line := range missingKeyLines(res.NeedKey) {
			fmt.Println("  " + line)
		}
	}
	if len(res.Skipped) > 0 {
		fmt.Printf("%s 跳过 %d 项：\n", amber.Render("!"), len(res.Skipped))
		for _, s := range res.Skipped {
			fmt.Println("  - " + s)
		}
	}
}

// missingKeyLines gives, for each provider name, the command that adds its key.
func missingKeyLines(names []string) []string {
	stored := provider.Stored()
	var out []string
	for _, name := range names {
		id := ""
		for _, p := range stored {
			if p.Name == name {
				id = p.ID
				break
			}
		}
		if id == "" {
			out = append(out, name)
			continue
		}
		out = append(out, fmt.Sprintf("%s  →  magpie provider key %s <key>", name, id))
	}
	return out
}
