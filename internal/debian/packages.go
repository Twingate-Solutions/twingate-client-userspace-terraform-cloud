// Package debian implements the small subset of Debian packaging needed to
// fetch a binary without root: parsing an apt Packages index, comparing
// versions, and reading files out of a .deb archive.
package debian

import (
	"bufio"
	"io"
	"strings"
)

// Stanza is one paragraph of a Packages index, keyed by field name.
type Stanza map[string]string

// Package returns the value of the Package field.
func (s Stanza) Package() string { return s["Package"] }

// Version returns the value of the Version field.
func (s Stanza) Version() string { return s["Version"] }

// Architecture returns the value of the Architecture field.
func (s Stanza) Architecture() string { return s["Architecture"] }

// Filename returns the pool path of the .deb, relative to the repository root.
func (s Stanza) Filename() string { return s["Filename"] }

// SHA256 returns the hex SHA-256 of the .deb.
func (s Stanza) SHA256() string { return strings.ToLower(s["SHA256"]) }

// ParsePackages parses an apt Packages index. Continuation lines are folded
// into the preceding field.
func ParsePackages(r io.Reader) ([]Stanza, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var (
		out     []Stanza
		cur     Stanza
		lastKey string
	)
	flush := func() {
		if len(cur) > 0 {
			out = append(out, cur)
		}
		cur = nil
		lastKey = ""
	}

	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if cur != nil && lastKey != "" {
				cur[lastKey] += "\n" + strings.TrimSpace(line)
			}
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if cur == nil {
			cur = Stanza{}
		}
		lastKey = strings.TrimSpace(key)
		cur[lastKey] = strings.TrimSpace(val)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flush()
	return out, nil
}

// Select returns the stanza for pkg/arch at the given version, or the highest
// version when version is empty. It returns nil when nothing matches.
func Select(stanzas []Stanza, pkg, arch, version string) Stanza {
	var best Stanza
	for _, s := range stanzas {
		if s.Package() != pkg || s.Architecture() != arch {
			continue
		}
		if version != "" {
			if s.Version() == version {
				return s
			}
			continue
		}
		if best == nil || CompareVersions(s.Version(), best.Version()) > 0 {
			best = s
		}
	}
	return best
}
