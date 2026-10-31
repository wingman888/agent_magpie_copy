package edit

import (
	"regexp"
	"strconv"
	"strings"
)

// Whole TOML tables are treated as units: magpie owns the tables it writes
// (for example a model provider) and replaces them verbatim, while every
// other line of the file stays untouched.

var tomlHeader = regexp.MustCompile(`^\s*\[\s*([^\]]+?)\s*\]`)

// TOMLTables lists the table headers of a TOML file in order.
func TOMLTables(path string) []string {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return nil
	}
	var out []string
	for _, line := range splitLines(string(raw)) {
		if m := tomlHeader.FindStringSubmatch(line); m != nil && !strings.HasPrefix(m[1], "[") {
			out = append(out, m[1])
		}
	}
	return out
}

// GetTOMLTable returns the scalar keys of one table.
func GetTOMLTable(path, name string) map[string]string {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return nil
	}
	lines := splitLines(string(raw))
	from, to := tableSpan(lines, name)
	if from < 0 {
		return nil
	}
	out := map[string]string{}
	for _, line := range lines[from+1 : to] {
		if m := tomlKV.FindStringSubmatch(line); m != nil {
			out[strings.Trim(m[1], `"`)] = tomlValue(m[2])
		}
	}
	return out
}

// SetTOMLTable replaces table `name` with the given keys, or appends it.
func SetTOMLTable(path, name string, kvs ...KV) error {
	raw, err := Read(path)
	if err != nil {
		return err
	}
	block := []string{"[" + name + "]"}
	for _, kv := range kvs {
		block = append(block, kv.Path+" = "+tomlLiteral(kv.Value))
	}
	lines := splitLines(string(raw))
	from, to := tableSpan(lines, name)
	var out []string
	if from < 0 {
		out = trimBlank(lines)
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, block...)
		out = append(out, "")
	} else {
		out = append(out, lines[:from]...)
		out = append(out, block...)
		if to < len(lines) && strings.TrimSpace(lines[to]) != "" {
			out = append(out, "")
		}
		out = append(out, lines[to:]...)
	}
	return WriteAtomic(path, []byte(joinLines(out)))
}

// DelTOMLTable removes table `name` (header and body) if present.
func DelTOMLTable(path, name string) error {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return err
	}
	lines := splitLines(string(raw))
	from, to := tableSpan(lines, name)
	if from < 0 {
		return nil
	}
	// Take the blank lines before the header with it so no gap is left.
	for from > 0 && strings.TrimSpace(lines[from-1]) == "" {
		from--
	}
	out := append([]string{}, lines[:from]...)
	if to < len(lines) && len(out) > 0 && strings.TrimSpace(lines[to]) != "" {
		out = append(out, "")
	}
	out = append(out, lines[to:]...)
	return WriteAtomic(path, []byte(joinLines(out)))
}

// DelTOMLTop removes top-level (pre-table) keys.
func DelTOMLTop(path string, keys ...string) error {
	raw, err := Read(path)
	if err != nil || raw == nil {
		return err
	}
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var out []string
	header := true
	for _, line := range splitLines(string(raw)) {
		if header && tomlTable.MatchString(line) {
			header = false
		}
		if header {
			if m := tomlKV.FindStringSubmatch(line); m != nil && drop[strings.Trim(m[1], `"`)] {
				continue
			}
		}
		out = append(out, line)
	}
	return WriteAtomic(path, []byte(joinLines(out)))
}

// tableSpan returns the line range [from, to) of a table: its header line
// through the line before the next header. from is -1 when absent.
func tableSpan(lines []string, name string) (int, int) {
	from := -1
	for i, line := range lines {
		m := tomlHeader.FindStringSubmatch(line)
		if m == nil || strings.HasPrefix(m[1], "[") {
			continue
		}
		if from >= 0 {
			return from, i
		}
		if m[1] == name {
			from = i
		}
	}
	if from < 0 {
		return -1, -1
	}
	return from, len(lines)
}

func trimBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func tomlLiteral(v any) string {
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	}
	return strconv.Quote(toString(v))
}
