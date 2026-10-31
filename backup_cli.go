package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/yetone/magpie/internal/backup"
)

const backupUsage = "usage: magpie backup [--no-keys] [file]"
const restoreUsage = "usage: magpie restore [--no-agents] <file>"

// readPass reads a passphrase: hidden from a terminal, the first line of
// stdin otherwise. The text is returned as typed, never echoed or logged.
func readPass(prompt string) (string, error) {
	fd := os.Stdin.Fd()
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", nil
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, prompt)
	st, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer func() {
		term.Restore(fd, st)
		fmt.Fprintln(os.Stderr)
	}()
	var pass []byte
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			if len(pass) > 0 {
				break
			}
			return "", errors.New("could not read the passphrase")
		}
		switch c := buf[0]; c {
		case '\r', '\n':
			return string(pass), nil
		case 3, 4: // ctrl-C, ctrl-D
			return "", errors.New("cancelled")
		case 127, 8:
			if len(pass) > 0 {
				pass = pass[:len(pass)-1]
			}
		default:
			pass = append(pass, c)
		}
	}
	return string(pass), nil
}

func backupCmd(args []string) error {
	keys := true
	var file string
	for _, a := range args {
		switch {
		case a == "--no-keys":
			keys = false
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q\n%s", a, backupUsage)
		case file != "":
			return fmt.Errorf("too many arguments\n%s", backupUsage)
		default:
			file = a
		}
	}
	if file == "" {
		file = "magpie" + backup.Ext
	}
	interactive := term.IsTerminal(os.Stdin.Fd())
	pass, err := readPass("Passphrase / 口令: ")
	if err != nil {
		return err
	}
	if pass == "" {
		return errors.New("the passphrase is empty / 口令不能为空")
	}
	if interactive {
		again, err := readPass("Again / 再输入一次: ")
		if err != nil {
			return err
		}
		if again != pass {
			return errors.New("the two passphrases differ / 两次口令不一致")
		}
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
	withKey, noKey := 0, 0
	for _, p := range b.Providers {
		if p.Key != "" || len(p.Keys) > 0 {
			withKey++
		} else {
			noKey++
		}
	}
	fmt.Println(green.Render("✓"), "saved", bold.Render(file), muted.Render(fmt.Sprintf(
		"· %d providers (%d with keys / 含 Key, %d without / 不含 Key) · %d profiles · %d agent settings",
		len(b.Providers), withKey, noKey, len(b.Profiles), len(b.Agents))))
	fmt.Println(muted.Render("  订阅不在其中,需在另一台机器登录;口令丢失则文件无法打开。"))
	return nil
}

// writePrivate writes data to path, readable by the owner alone.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		_ = err // not supported everywhere (Windows)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return os.Chmod(path, 0o600)
}

func restoreCmd(args []string) error {
	parts := backup.All
	var file string
	for _, a := range args {
		switch {
		case a == "--no-agents":
			parts.Agents = false
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q\n%s", a, restoreUsage)
		case file != "":
			return fmt.Errorf("too many arguments\n%s", restoreUsage)
		default:
			file = a
		}
	}
	if file == "" {
		return errors.New(restoreUsage)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	pass, err := readPass("Passphrase / 口令: ")
	if err != nil {
		return err
	}
	if pass == "" {
		return errors.New("the passphrase is empty / 口令不能为空")
	}
	b, err := backup.Open(data, pass)
	if err != nil {
		return err
	}
	r, err := backup.Restore(b, parts)
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "restored", bold.Render(file))
	fmt.Printf("  providers: %d added, %d replaced\n", r.Added, r.Replaced)
	if r.Settings {
		fmt.Println("  settings: restored")
	} else {
		fmt.Println("  settings: not restored")
	}
	fmt.Printf("  profiles: %d\n", r.Profiles)
	fmt.Printf("  agent settings: %d changed\n", r.Agents)
	if len(r.NeedKey) > 0 {
		fmt.Println(amber.Render("!"), "no key yet / 需要补 Key:", strings.Join(r.NeedKey, ", "))
		fmt.Println(muted.Render("  magpie provider key <id> <key>"))
	}
	if len(r.Skipped) > 0 {
		fmt.Println(amber.Render("!"), "skipped / 已跳过 (本机未安装该 Agent,或其模型暂时不可用):")
		for _, s := range r.Skipped {
			fmt.Println("  -", s)
		}
	}
	fmt.Println(muted.Render("  已运行的 Agent 需重启后生效;订阅需在本机重新登录。"))
	return nil
}
