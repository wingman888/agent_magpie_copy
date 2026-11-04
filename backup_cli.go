package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/backup"
)

const (
	backupUsage  = "usage: magpie backup [--no-keys] [file]"
	restoreUsage = "usage: magpie restore [--no-agents] <file>"
)

var (
	errEmptyPassphrase    = errors.New("口令不能为空")
	errPassphraseMismatch = errors.New("两次输入的口令不一致")
)

// backupOpts is what `magpie backup` was asked to do.
type backupOpts struct {
	noKeys bool
	file   string
}

// parseBackupArgs reads `magpie backup [--no-keys] [file]`.
func parseBackupArgs(args []string) (backupOpts, error) {
	o := backupOpts{file: "magpie" + backup.Ext}
	files := 0
	for _, a := range args {
		switch {
		case a == "--no-keys":
			o.noKeys = true
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("未知参数 %s\n%s", a, backupUsage)
		default:
			if files > 0 {
				return o, fmt.Errorf("多余的参数 %s\n%s", a, backupUsage)
			}
			o.file = a
			files++
		}
	}
	return o, nil
}

// restoreOpts is what `magpie restore` was asked to do.
type restoreOpts struct {
	noAgents bool
	file     string
}

// parseRestoreArgs reads `magpie restore [--no-agents] <file>`.
func parseRestoreArgs(args []string) (restoreOpts, error) {
	var o restoreOpts
	files := 0
	for _, a := range args {
		switch {
		case a == "--no-agents":
			o.noAgents = true
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("未知参数 %s\n%s", a, restoreUsage)
		default:
			if files > 0 {
				return o, fmt.Errorf("多余的参数 %s\n%s", a, restoreUsage)
			}
			o.file = a
			files++
		}
	}
	if files == 0 {
		return o, fmt.Errorf("缺少备份文件\n%s", restoreUsage)
	}
	return o, nil
}

// backupCmd: `magpie backup [--no-keys] [file]` writes every setting into one
// passphrase-sealed file.
func backupCmd(args []string) error {
	o, err := parseBackupArgs(args)
	if err != nil {
		return err
	}
	pass, err := readPassphrase("口令: ", stdinIsTerminal(), os.Stdin, os.Stderr, true)
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
	if err := os.WriteFile(o.file, data, 0o600); err != nil {
		return err
	}
	// WriteFile sets the mode only when it creates the file.
	if err := os.Chmod(o.file, 0o600); err != nil {
		return err
	}

	keys := "含 Key"
	if o.noKeys {
		keys = "不含 Key"
	}
	fmt.Println(green.Render("✓"), "已备份到", bold.Render(o.file))
	fmt.Printf("  %d 个提供商（%s） · %d 个方案 · %d 项 Agent 设置\n",
		len(b.Providers), keys, countProfiles(b.Profiles), countAgentSettings(b.Agents))
	fmt.Println(muted.Render("  订阅不在备份中：在另一台机器上需要重新登录"))
	fmt.Println(muted.Render("  口令丢失则无法打开此文件"))
	return nil
}

// restoreCmd: `magpie restore [--no-agents] <file>` puts a backup back.
func restoreCmd(args []string) error {
	o, err := parseRestoreArgs(args)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(o.file)
	if err != nil {
		return err
	}
	// Report a file that is not a backup, or is from a newer magpie, before
	// asking for a passphrase. An empty passphrase never opens a backup, so
	// Open can only fail here with ErrPassphrase or a format error.
	if _, err := backup.Open(data, ""); err != nil && !errors.Is(err, backup.ErrPassphrase) {
		return backupOpenError(err)
	}
	pass, err := readPassphrase("口令: ", stdinIsTerminal(), os.Stdin, os.Stderr, false)
	if err != nil {
		return err
	}
	b, err := backup.Open(data, pass)
	if errors.Is(err, backup.ErrPassphrase) {
		return errors.New("口令错误或文件已被改动，未做任何更改")
	}
	if err != nil {
		return backupOpenError(err)
	}

	parts := backup.All
	parts.Agents = !o.noAgents
	r, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}
	printRestore(r)
	return nil
}

// backupOpenError turns the format errors of backup.Open into messages for
// the user. Anything else is returned as it is.
func backupOpenError(err error) error {
	switch {
	case errors.Is(err, backup.ErrNotBackup):
		return errors.New("不是 magpie 备份文件")
	case errors.Is(err, backup.ErrNewerVersion):
		return errors.New("该备份由更新版本的 magpie 生成，请升级 magpie")
	}
	return err
}

// printRestore reports what a restore did, and what the user still has to do.
func printRestore(r backup.Result) {
	fmt.Println(green.Render("✓"), "已恢复备份")
	fmt.Printf("  提供商：新增 %d 个，替换 %d 个\n", r.Added, r.Replaced)
	if r.Settings {
		fmt.Println("  设置：已恢复")
	} else {
		fmt.Println("  设置：备份中没有设置，保留本机设置")
	}
	fmt.Printf("  方案：%d 个\n", r.Profiles)
	fmt.Printf("  Agent 设置：变更 %d 项\n", r.Agents)
	if len(r.NeedKey) > 0 {
		fmt.Println(amber.Render("!"), "以下提供商还没有 Key："+strings.Join(r.NeedKey, "、"))
		fmt.Println(muted.Render("  用 magpie provider key <id> 为以上提供商补充 Key"))
	}
	if len(r.Skipped) > 0 {
		fmt.Println(amber.Render("!"), "以下 Agent 设置被跳过：")
		for _, s := range r.Skipped {
			fmt.Println("  ·", s)
		}
	}
}

// countProfiles is the number of profiles in a backup's profiles part.
func countProfiles(raw json.RawMessage) int {
	var ps map[string]json.RawMessage
	_ = json.Unmarshal(raw, &ps)
	return len(ps)
}

// countAgentSettings is the number of agent settings in a backup's agents part.
func countAgentSettings(raw json.RawMessage) int {
	var ags map[string]map[string]string
	_ = json.Unmarshal(raw, &ags)
	n := 0
	for _, vals := range ags {
		n += len(vals)
	}
	return n
}

// stdinIsTerminal reports whether standard input is a terminal.
func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// checkPassphrase rejects an empty passphrase, and, when confirm is set, a
// second entry that differs from the first.
func checkPassphrase(first, second string, confirm bool) error {
	if first == "" {
		return errEmptyPassphrase
	}
	if confirm && first != second {
		return errPassphraseMismatch
	}
	return nil
}

// readPassphrase takes a passphrase. On a terminal (isTerm) it is typed with
// echo off, twice when confirm is set. Otherwise it is the first line of in,
// and confirm is ignored. The passphrase is never printed.
func readPassphrase(prompt string, isTerm bool, in io.Reader, out io.Writer, confirm bool) (string, error) {
	if !isTerm {
		p, err := readLine(in)
		if err != nil {
			return "", err
		}
		if err := checkPassphrase(p, "", false); err != nil {
			return "", err
		}
		return p, nil
	}
	f, ok := in.(*os.File)
	if !ok {
		return "", errors.New("口令需要从终端输入")
	}
	first, err := readSecret(f, prompt, out)
	if err != nil {
		return "", err
	}
	if err := checkPassphrase(first, "", false); err != nil {
		return "", err
	}
	if confirm {
		second, err := readSecret(f, "再输入一次口令: ", out)
		if err != nil {
			return "", err
		}
		if err := checkPassphrase(first, second, true); err != nil {
			return "", err
		}
	}
	return first, nil
}

// readSecret reads one line from the terminal f with echo off.
func readSecret(f *os.File, prompt string, out io.Writer) (string, error) {
	fmt.Fprint(out, prompt)
	restore, err := hideEcho(f)
	if err != nil {
		return "", fmt.Errorf("无法关闭终端回显，未读取口令: %w", err)
	}
	defer restore()
	line, err := readLine(f)
	fmt.Fprintln(out)
	return line, err
}

// readLine returns the first line of r without its line ending.
func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
