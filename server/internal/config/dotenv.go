package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// loadDotEnv applies the KEY=VALUE lines of the file at path to the process
// environment, so editing .env next to the binary is enough to configure it.
// Variables that are already set in the environment win. A missing file is
// not an error; loaded reports whether one was read.
//
// Accepted syntax, a subset of what a shell would source: blank lines and
// "#" comments, an optional "export " prefix, values in single quotes
// (literal) or double quotes (with \n, \" and \\ escapes), and unquoted
// values, where " #" starts a trailing comment.
func loadDotEnv(path string) (loaded bool, err error) {
	f, err := os.Open(path) //nolint:gosec // G304: a fixed file name in the working directory
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t\"'") {
			return false, fmt.Errorf("%s line %d: expected KEY=VALUE", path, n)
		}
		value, err := parseDotEnvValue(strings.TrimSpace(raw))
		if err != nil {
			return false, fmt.Errorf("%s line %d: %w", path, n, err)
		}
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return false, fmt.Errorf("%s line %d: %w", path, n, err)
		}
	}
	return true, sc.Err()
}

func parseDotEnvValue(raw string) (string, error) {
	switch {
	case strings.HasPrefix(raw, "'"):
		end := strings.IndexByte(raw[1:], '\'')
		if end < 0 {
			return "", errors.New("unterminated single quote")
		}
		return raw[1 : 1+end], nil
	case strings.HasPrefix(raw, `"`):
		var b strings.Builder
		for i := 1; i < len(raw); i++ {
			switch c := raw[i]; {
			case c == '"':
				return b.String(), nil
			case c == '\\' && i+1 < len(raw):
				i++
				switch raw[i] {
				case 'n':
					b.WriteByte('\n')
				case '"', '\\':
					b.WriteByte(raw[i])
				default:
					b.WriteByte('\\')
					b.WriteByte(raw[i])
				}
			default:
				b.WriteByte(c)
			}
		}
		return "", errors.New("unterminated double quote")
	}
	// Unquoted: a "#" after whitespace starts a comment.
	for i := 1; i < len(raw); i++ {
		if raw[i] == '#' && (raw[i-1] == ' ' || raw[i-1] == '\t') {
			raw = raw[:i]
			break
		}
	}
	if strings.HasPrefix(raw, "#") {
		return "", nil
	}
	return strings.TrimSpace(raw), nil
}
