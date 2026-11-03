package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

const errPassNotHidden = "无法关闭终端回显（%v），请改用管道输入口令，例如 echo 口令 | magpie backup"

var (
	errBackupUsage  = errors.New("usage: magpie backup [--no-keys] [file]")
	errRestoreUsage = errors.New("usage: magpie restore [--no-agents] <file>")
	errEmptyPass    = errors.New("口令不能为空，未写入任何文件")
	errRestoreEmpty = errors.New("口令不能为空")
	errPassMismatch = errors.New("两次输入的口令不一致，未生成备份")
)

// backupCmd: `magpie backup [--no-keys] [file]`
func backupCmd(args []string) error {
	keys, file, err := parseBackupArgs(args[1:])
	if err != nil {
		return err
	}
	pass, err := newPassphrase()
	if err != nil {
		return err
	}
	b, err := backup.Collect(keys, version)
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
	agents := make([]string, 0, len(b.Agents))
	for id := range b.Agents {
		agents = append(agents, id)
	}
	sort.Strings(agents)
	settings := "无"
	if b.Settings != nil {
		settings = "有"
	}
	fmt.Println(green.Render("✓"), "备份已写入", bold.Render(file))
	fmt.Printf("  提供商 %d 个（含 Key %d 个，不含 Key %d 个）\n", len(b.Providers), withKey, len(b.Providers)-withKey)
	fmt.Printf("  方案 %d 个\n", len(b.Profiles))
	fmt.Printf("  Agent 设置 %d 个 Agent", len(agents))
	if len(agents) > 0 {
		fmt.Printf("（%s）", strings.Join(agents, ", "))
	}
	fmt.Println()
	fmt.Printf("  设置：%s\n", settings)
	fmt.Println(muted.Render("  订阅账号不在备份中，需在另一台机器上分别登录。"))
	fmt.Println(muted.Render("  口令丢失则无法打开此文件，请妥善保管。"))
	return nil
}

// restoreCmd: `magpie restore [--no-agents] <file>`
func restoreCmd(args []string) error {
	agents, file, err := parseRestoreArgs(args[1:])
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	// A file that is not a backup, or needs a newer magpie, is refused before
	// anyone is asked for a passphrase.
	if err := backup.CheckFormat(data); err != nil {
		return openError(err)
	}
	pass, err := askPassphrase("口令：")
	if err != nil {
		return err
	}
	if pass == "" {
		return errRestoreEmpty
	}
	b, err := backup.Open(data, pass)
	if err != nil {
		return openError(err)
	}
	// The passphrase is right, so this machine may now get the startup
	// migrations that every other command gets. Nothing above has written
	// anything. With --no-agents the agents' own files are not touched at all.
	settings.Migrate()
	if agents {
		agent.RenameLegacy()
	}
	parts := backup.All
	parts.Agents = agents
	res, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}
	printRestore(res, agents)
	return nil
}

// openError words a backup that cannot be opened. A wrong passphrase and a
// changed file look the same to magpie, so both get one message. The other
// errors (not a backup, a newer magpie) are already clear and pass through.
func openError(err error) error {
	if errors.Is(err, backup.ErrPassphrase) {
		return errors.New("口令错误或文件已被改动，未修改本机任何配置")
	}
	return err
}

func printRestore(r backup.Result, agents bool) {
	fmt.Printf("%s 提供商：新增 %d 个，替换 %d 个\n", green.Render("✓"), r.Added, r.Replaced)
	if r.Settings {
		fmt.Println(green.Render("✓"), "设置：已恢复")
	} else {
		fmt.Println(muted.Render("  设置：备份中没有，未改动"))
	}
	fmt.Printf("方案：%d 个\n", r.Profiles)
	if agents {
		fmt.Printf("Agent 设置：变更 %d 项\n", r.Agents)
	} else {
		fmt.Println(muted.Render("  Agent 设置：未恢复（--no-agents）"))
	}
	if len(r.NeedKey) > 0 {
		fmt.Println(amber.Render("以下提供商还没有可用的 Key，请用 magpie provider key <id> 补充："))
		for _, line := range needKeyLines(r.NeedKey) {
			fmt.Println("  " + line)
		}
	}
	if len(r.Skipped) > 0 {
		fmt.Println(amber.Render("已跳过的 Agent 设置："))
		for _, s := range r.Skipped {
			fmt.Println("  " + s)
		}
	}
}

// needKeyLines maps provider names to their ids, which is what the key
// command takes.
func needKeyLines(names []string) []string {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var lines []string
	found := map[string]bool{}
	for _, p := range provider.All() {
		if !want[p.Name] || found[p.Name] || p.Account != nil || p.Key != "" || len(p.Keys) > 0 {
			continue
		}
		found[p.Name] = true
		lines = append(lines, fmt.Sprintf("%s  →  magpie provider key %s", p.Name, p.ID))
	}
	for _, n := range names {
		if !found[n] {
			lines = append(lines, n)
		}
	}
	return lines
}

// parseBackupArgs reads the arguments after "backup".
func parseBackupArgs(args []string) (keys bool, file string, err error) {
	keys = true
	n := 0
	for _, a := range args {
		switch {
		case a == "--no-keys":
			keys = false
		case a == "" || strings.HasPrefix(a, "-"):
			return false, "", errBackupUsage
		default:
			n++
			file = a
		}
	}
	if n > 1 {
		return false, "", errBackupUsage
	}
	if file == "" {
		file = "magpie" + backup.Ext
	}
	return keys, file, nil
}

// parseRestoreArgs reads the arguments after "restore".
func parseRestoreArgs(args []string) (agents bool, file string, err error) {
	agents = true
	n := 0
	for _, a := range args {
		switch {
		case a == "--no-agents":
			agents = false
		case a == "" || strings.HasPrefix(a, "-"):
			return false, "", errRestoreUsage
		default:
			n++
			file = a
		}
	}
	if n != 1 {
		return false, "", errRestoreUsage
	}
	return agents, file, nil
}

// newPassphrase asks for the passphrase for a new backup: twice on a
// terminal, once (the first line of stdin) otherwise.
func newPassphrase() (string, error) {
	p, err := askPassphrase("口令：")
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", errEmptyPass
	}
	if stdinIsTerminal() {
		again, err := readSecret("再输入一次：")
		if err != nil {
			return "", err
		}
		if again != p {
			return "", errPassMismatch
		}
	}
	return p, nil
}

// askPassphrase reads one passphrase: without echo on a terminal, otherwise
// the first line of stdin.
func askPassphrase(prompt string) (string, error) {
	if stdinIsTerminal() {
		return readSecret(prompt)
	}
	return readLine(os.Stdin)
}

func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// readSecret reads one line from the terminal with echo off. The prompt goes
// to stderr; the passphrase is never printed.
func readSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	restore, err := hideEcho()
	if err != nil {
		fmt.Fprintln(os.Stderr)
		return "", fmt.Errorf(errPassNotHidden, err)
	}
	stop := catchInterrupt(restore)
	line, err := readLine(os.Stdin)
	stop()
	fmt.Fprintln(os.Stderr)
	return line, err
}

// catchInterrupt turns echo back on if Ctrl-C arrives while the passphrase
// is typed. The returned function ends the watch and restores the terminal.
func catchInterrupt(restore func()) func() {
	sig := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(sig, os.Interrupt)
	go func() {
		select {
		case <-sig:
			restore()
			os.Exit(130)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sig)
		close(done)
		restore()
	}
}

// readLine returns the first line of r without its line ending.
func readLine(f *os.File) (string, error) {
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	return strings.TrimSuffix(line, "\r"), nil
}

// writeBackupFile writes the backup to a temporary file with the owner's
// access only, then moves it into place, so an old file is never half
// overwritten.
//
// Windows: the 0600 mode is not enforced there (Go only tracks the read-only
// bit), so the file gets the access rights of its folder. The encryption is
// what protects the contents; the file's own permissions are not changed.
func writeBackupFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".magpie-backup-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // gone after the rename; harmless otherwise
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
