// Package vmx parses the line-oriented VMware configuration format. Unknown or
// ambiguous syntax is rejected instead of silently dropping configuration.
package vmx

import (
	"bufio"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Config map[string]string

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9_.:-]+$`)

func Parse(text string) (Config, error) {
	c := Config{}
	s := bufio.NewScanner(strings.NewReader(text))
	s.Buffer(make([]byte, 4096), 1<<20)
	line := 0
	for s.Scan() {
		line++
		t := strings.TrimSpace(s.Text())
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if !ok || !keyPattern.MatchString(k) || !strings.HasPrefix(v, `"`) {
			return nil, fmt.Errorf("invalid configuration at line %d", line)
		}
		end := strings.Index(v[1:], `"`)
		if end < 0 {
			return nil, fmt.Errorf("unterminated value at line %d", line)
		}
		end++
		rest := strings.TrimSpace(v[end+1:])
		if rest != "" && !strings.HasPrefix(rest, "#") {
			return nil, fmt.Errorf("trailing data at line %d", line)
		}
		value, err := decode(v[1:end])
		if err != nil {
			return nil, fmt.Errorf("invalid escape at line %d", line)
		}
		if _, ok := c[k]; ok {
			return nil, fmt.Errorf("duplicate key %s", k)
		}
		c[k] = value
	}
	if s.Err() != nil {
		return nil, s.Err()
	}
	if len(c) == 0 {
		return nil, fmt.Errorf("empty configuration")
	}
	if encoding, ok := c[".encoding"]; ok && !strings.EqualFold(encoding, "UTF-8") {
		return nil, fmt.Errorf("unsupported configuration encoding")
	}
	return c, nil
}
func decode(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			if i+2 >= len(s) {
				return "", fmt.Errorf("invalid escape")
			}
			n, e := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if e != nil {
				return "", e
			}
			b.WriteByte(byte(n))
			i += 2
		} else {
			b.WriteByte(s[i])
		}
	}
	if !utf8.ValidString(b.String()) || strings.ContainsRune(b.String(), 0) {
		return "", fmt.Errorf("invalid text")
	}
	return b.String(), nil
}
func encode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '|' || ch == '"' || ch < 32 || ch == 127 {
			fmt.Fprintf(&b, "|%02X", ch)
		} else {
			b.WriteByte(ch)
		}
	}
	return b.String()
}
func (c Config) String() string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = \"%s\"\n", k, encode(c[k]))
	}
	return b.String()
}
func (c Config) Clone() Config {
	out := Config{}
	for k, v := range c {
		out[k] = v
	}
	return out
}
func (c Config) True(k string) bool { return strings.EqualFold(c[strings.ToLower(k)], "true") }
