package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

const (
	defaultBackupFile = "magpie" + backup.Ext
	backupUsage       = "usage: magpie backup [--no-keys] [file]   (default file: " + defaultBackupFile + ")"
	restoreUsage      = "usage: magpie restore [--no-agents] <file>"
)

// parseBackupArgs reads `magpie backup [--no-keys] [file]`.
func parseBackupArgs(args []string) (file string, noKeys bool, err error) {
	var pos []string
	for _, a := range args {
		switch {
		case a == "--no-keys":
			noKeys = true
		case strings.HasPrefix(a, "-"):
			return "", false, fmt.Errorf("unknown option %q\n%s", a, backupUsage)
		default:
			pos = append(pos, a)
		}
	}
	switch len(pos) {
	case 0:
		return defaultBackupFile, noKeys, nil
	case 1:
		if pos[0] == "" {
			return "", false, fmt.Errorf("empty file name\n%s", backupUsage)
		}
		return pos[0], noKeys, nil
	}
	return "", false, fmt.Errorf("too many arguments\n%s", backupUsage)
}

// parseRestoreArgs reads `magpie restore [--no-agents] <file>`.
func parseRestoreArgs(args []string) (file string, noAgents bool, err error) {
	var pos []string
	for _, a := range args {
		switch {
		case a == "--no-agents":
			noAgents = true
		case strings.HasPrefix(a, "-"):
			return "", false, fmt.Errorf("unknown option %q\n%s", a, restoreUsage)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 || pos[0] == "" {
		return "", false, fmt.Errorf("%s", restoreUsage)
	}
	return pos[0], noAgents, nil
}

// backupCmd: `magpie backup [--no-keys] [file]`
func backupCmd(args []string) error {
	file, noKeys, err := parseBackupArgs(args)
	if err != nil {
		return err
	}
	pass, err := askPassphrase(true)
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
	printBackupSummary(file, b)
	return nil
}

// writeBackupFile writes the sealed backup readable and writable by the owner only.
func writeBackupFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	// WriteFile keeps the mode of an existing file, so set it explicitly.
	return os.Chmod(path, 0o600)
}

// backupCounts describes what a bundle holds, for the summary line.
type backupCounts struct {
	providers, withKey, profiles, agents int
}

func countBundle(b backup.Bundle) backupCounts {
	c := backupCounts{providers: len(b.Providers)}
	for _, p := range b.Providers {
		if p.Key != "" || len(p.Keys) > 0 {
			c.withKey++
		}
	}
	if len(b.Profiles) > 0 {
		var ps map[string]json.RawMessage
		if json.Unmarshal(b.Profiles, &ps) == nil {
			c.profiles = len(ps)
		}
	}
	if len(b.Agents) > 0 {
		var ag map[string]string
		if json.Unmarshal(b.Agents, &ag) == nil {
			c.agents = len(ag)
		}
	}
	return c
}

func printBackupSummary(file string, b backup.Bundle) {
	c := countBundle(b)
	fmt.Println(green.Render("✓"), "已备份到", bold.Render(file))
	fmt.Printf("  提供商 %d 个（含 Key %d 个，不含 Key %d 个）· 方案 %d 个 · Agent 设置 %d 项\n",
		c.providers, c.withKey, c.providers-c.withKey, c.profiles, c.agents)
	fmt.Println(muted.Render("  订阅登录不在备份中，需在另一台机器上用 magpie accounts add 重新登录。"))
	fmt.Println(muted.Render("  口令丢失后将无法打开此文件。"))
}

// restoreCmd: `magpie restore [--no-agents] <file>`
func restoreCmd(args []string) error {
	file, noAgents, err := parseRestoreArgs(args)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read backup: %w", err)
	}
	pass, err := askPassphrase(false)
	if err != nil {
		return err
	}
	b, err := backup.Open(data, pass)
	switch {
	case errors.Is(err, backup.ErrPassphrase):
		return errors.New("口令错误，或备份文件已被改动；本机配置未做任何修改")
	case err != nil:
		// ErrNotBackup and ErrNewer already carry a clear message.
		return err
	}
	res, err := backup.Restore(b, backup.Parts{Providers: true, Settings: true, Profiles: true, Agents: !noAgents})
	if err != nil {
		return err
	}
	printRestoreSummary(file, res, noAgents)
	return nil
}

func printRestoreSummary(file string, res backup.Result, noAgents bool) {
	fmt.Println(green.Render("✓"), "已从", bold.Render(file), "恢复")
	fmt.Printf("  提供商：新增 %d 个，替换 %d 个\n", res.Added, res.Replaced)
	if res.Settings {
		fmt.Println("  设置：已恢复")
	} else {
		fmt.Println("  设置：备份中没有，未改动")
	}
	fmt.Printf("  方案：恢复 %d 个\n", res.Profiles)
	if noAgents {
		fmt.Println("  Agent 设置：未恢复（--no-agents）")
	} else {
		fmt.Printf("  Agent 设置：变更 %d 项\n", res.Agents)
	}

	if len(res.NeedKey) > 0 {
		ids := map[string]string{}
		for _, p := range provider.All() {
			if p.Account == nil {
				ids[p.Name] = p.ID
			}
		}
		fmt.Println(amber.Render("!"), "以下提供商还没有 Key，请用 magpie provider key <id> 补充：")
		for _, name := range res.NeedKey {
			if id := ids[name]; id != "" {
				fmt.Printf("    - %s  (magpie provider key %s)\n", name, id)
			} else {
				fmt.Printf("    - %s\n", name)
			}
		}
	}
	if len(res.Skipped) > 0 {
		fmt.Println(amber.Render("!"), "以下 Agent 设置被跳过：")
		for _, s := range res.Skipped {
			fmt.Println("    -", s)
		}
	}
}
