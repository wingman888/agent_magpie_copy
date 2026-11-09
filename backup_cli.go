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
	"github.com/yetone/magpie/internal/provider"
)

const backupUsage = `usage:
  magpie backup [--no-keys] [file]     export providers, icons, settings, profiles and agent models to one sealed file
                                       (default magpie.magpie-backup; --no-keys leaves out every API key and credential header)
  magpie restore [--no-agents] <file>  restore a file made by magpie backup (--no-agents: leave agent models alone)`

const restoreUsage = `usage: magpie restore [--no-agents] <file>`

// parseBackupArgs reads `magpie backup [--no-keys] [file]`; args excludes the
// command name.
func parseBackupArgs(args []string) (noKeys bool, file string, err error) {
	for _, a := range args {
		switch {
		case a == "--no-keys":
			noKeys = true
		case strings.HasPrefix(a, "-"):
			return false, "", fmt.Errorf("unknown option %q\n\n%s", a, backupUsage)
		case file != "":
			return false, "", fmt.Errorf("too many arguments\n\n%s", backupUsage)
		default:
			file = a
		}
	}
	if file == "" {
		file = "magpie" + backup.Ext
	}
	return noKeys, file, nil
}

// parseRestoreArgs reads `magpie restore [--no-agents] <file>`; args excludes
// the command name.
func parseRestoreArgs(args []string) (noAgents bool, file string, err error) {
	for _, a := range args {
		switch {
		case a == "--no-agents":
			noAgents = true
		case strings.HasPrefix(a, "-"):
			return false, "", fmt.Errorf("unknown option %q\n\n%s", a, restoreUsage)
		case file != "":
			return false, "", fmt.Errorf("too many arguments\n\n%s", restoreUsage)
		default:
			file = a
		}
	}
	if file == "" {
		return false, "", fmt.Errorf("missing file\n\n%s", restoreUsage)
	}
	return noAgents, file, nil
}

// backupCmd is `magpie backup [--no-keys] [file]`.
func backupCmd(args []string) error {
	noKeys, file, err := parseBackupArgs(args)
	if err != nil {
		return err
	}
	b, err := backup.Collect(!noKeys, version)
	if err != nil {
		return err
	}
	in := bufio.NewReader(os.Stdin)
	tty := isTerminal(os.Stdin)
	pass, err := readPass(in, tty, "口令: ")
	if err != nil {
		return err
	}
	if pass == "" {
		return errors.New("口令不能为空，未生成备份文件")
	}
	if tty {
		again, err := readPass(in, tty, "再输入一次: ")
		if err != nil {
			return err
		}
		if again != pass {
			return errors.New("两次输入的口令不一致，未生成备份文件")
		}
	}
	data, err := backup.Seal(b, pass)
	if err != nil {
		return err
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return err
	}
	// WriteFile keeps the mode of a file that already existed.
	if err := os.Chmod(file, 0o600); err != nil {
		return err
	}

	withKeys := 0
	for _, p := range b.Providers {
		if hasAnyKey(p) {
			withKeys++
		}
	}
	fmt.Println(green.Render("✓"), "wrote", bold.Render(file))
	fmt.Printf("  %d providers (%d with keys, %d without) · %d profiles · %d agent settings\n",
		len(b.Providers), withKeys, len(b.Providers)-withKeys, len(b.Profiles), len(b.Agents))
	fmt.Println(muted.Render("  subscriptions are not in the file: sign in to them on the other machine"))
	fmt.Println(muted.Render("  keep the passphrase: without it the file cannot be opened"))
	return nil
}

// restoreCmd is `magpie restore [--no-agents] <file>`.
func restoreCmd(args []string) error {
	noAgents, file, err := parseRestoreArgs(args)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	// Tell a file that is not a backup, or needs a newer magpie, before asking
	// for a passphrase it could never open.
	if err := checkBackupHeader(data); err != nil {
		return err
	}
	in := bufio.NewReader(os.Stdin)
	pass, err := readPass(in, isTerminal(os.Stdin), "口令: ")
	if err != nil {
		return err
	}
	b, err := backup.Open(data, pass)
	if errors.Is(err, backup.ErrPassphrase) {
		return errors.New("口令错误或文件已被改动，未修改本机配置")
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

	fmt.Println(green.Render("✓"), "restored", bold.Render(file))
	fmt.Printf("  providers: %d added, %d replaced\n", res.Added, res.Replaced)
	if res.Settings {
		fmt.Println("  settings: restored")
	} else {
		fmt.Println("  settings: none in the file")
	}
	fmt.Printf("  profiles: %d restored\n", res.Profiles)
	if noAgents {
		fmt.Println("  agents: left alone (--no-agents)")
	} else {
		fmt.Printf("  agents: %d changed\n", res.Agents)
	}

	if len(res.NeedKey) > 0 {
		ids := providerIDsByName(b.Providers)
		fmt.Println(amber.Render("!"), "these providers have no key yet:")
		for _, name := range res.NeedKey {
			if id, ok := ids[name]; ok {
				fmt.Printf("  %s   magpie provider key %s <key>\n", name, id)
			} else {
				fmt.Printf("  %s   magpie provider key <id> <key>\n", name)
			}
		}
	}
	if len(res.Skipped) > 0 {
		fmt.Println(amber.Render("!"), "skipped:")
		for _, s := range res.Skipped {
			fmt.Println("  " + s)
		}
	}
	return nil
}

// checkBackupHeader reports a file that is not a backup, or one a newer
// magpie made, using the same wording as backup.Open.
func checkBackupHeader(data []byte) error {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return errors.New("不是 magpie 备份文件")
	}
	var format string
	if json.Unmarshal(raw["format"], &format) != nil || format != "magpie-backup" {
		return errors.New("不是 magpie 备份文件")
	}
	var ver int
	if json.Unmarshal(raw["version"], &ver) == nil && ver > backup.FormatVersion {
		return errors.New("这个备份由更新版本的 magpie 生成，需升级 magpie 后再恢复")
	}
	return nil
}

// readPass reads one passphrase line. On a terminal the input is not echoed
// and the prompt goes to stderr; otherwise the first line of in is used.
func readPass(in *bufio.Reader, tty bool, prompt string) (string, error) {
	if !tty {
		line, err := in.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, prompt)
	restore, err := hideEcho()
	if err != nil {
		return "", err
	}
	line, err := in.ReadString('\n')
	restore()
	fmt.Fprintln(os.Stderr)
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// isTerminal reports whether f is a character device, as the other prompts
// in this package check.
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func hasAnyKey(p provider.Provider) bool {
	if p.Key != "" {
		return true
	}
	for _, k := range p.Keys {
		if k.Key != "" {
			return true
		}
	}
	return false
}

func providerIDsByName(ps []provider.Provider) map[string]string {
	ids := map[string]string{}
	for _, p := range ps {
		if _, dup := ids[p.Name]; !dup {
			ids[p.Name] = p.ID
		}
	}
	return ids
}
