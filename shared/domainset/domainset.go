// Package domainset normalizes and matches domain rules. It is the single
// source of truth shared by DNS rewrite, ingress routing and egress allowlist.
package domainset

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/idna"
)

type Kind string

const (
	KindExact  Kind = "exact"
	KindSuffix Kind = "suffix"
	KindRegex  Kind = "regex"
)

type Entry struct {
	Kind  Kind   `json:"kind"`
	Value string `json:"value"`
}

var idnaProfile = idna.New(idna.MapForLookup(), idna.StrictDomainName(false), idna.BidiRule())

var errEmpty = errors.New("empty")

// NormalizeHost lowercases, strips a single terminal dot and applies IDNA.
func NormalizeHost(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "\ufeff")
	if s == "" {
		return "", errEmpty
	}
	if strings.HasSuffix(s, ".") {
		s = s[:len(s)-1]
	}
	if s == "" {
		return "", errEmpty
	}
	a, err := idnaProfile.ToASCII(s)
	if err != nil {
		return "", fmt.Errorf("idna: %w", err)
	}
	a = strings.ToLower(a)
	if err := validateHost(a); err != nil {
		return "", err
	}
	return a, nil
}

func validateHost(h string) error {
	if len(h) == 0 || len(h) > 253 {
		return errors.New("bad length")
	}
	if strings.Contains(h, "..") {
		return errors.New("empty label")
	}
	for _, l := range strings.Split(h, ".") {
		if len(l) == 0 || len(l) > 63 {
			return errors.New("bad label length")
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
			if !ok {
				return fmt.Errorf("bad char %q", c)
			}
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return errors.New("label hyphen boundary")
		}
	}
	if !strings.Contains(h, ".") {
		return errors.New("needs at least two labels")
	}
	return nil
}

// ParseOptions controls the line parser.
type ParseOptions struct {
	AllowRegex bool
	MaxEntries int
}

// ParseResult is the outcome of parsing one source payload.
type ParseResult struct {
	Entries  []Entry
	Skipped  int
	Warnings []string
}

// ParseLines accepts plain lists, `domain:`, `domain-suffix:`, `full:`,
// wildcards and hosts-file style lines.
func ParseLines(body string, opt ParseOptions) (ParseResult, error) {
	var res ParseResult
	seen := map[Entry]bool{}
	max := opt.MaxEntries
	if max <= 0 {
		max = 1_000_000
	}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "//") {
			continue
		}
		if i := strings.IndexAny(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
			if line == "" {
				continue
			}
		}
		// hosts-file form: "0.0.0.0 example.com"
		if f := strings.Fields(line); len(f) == 2 && (f[0] == "0.0.0.0" || f[0] == "127.0.0.1" || f[0] == "::") {
			line = f[1]
		}
		e, err := parseOne(line, opt.AllowRegex)
		if err != nil {
			res.Skipped++
			if len(res.Warnings) < 50 {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%q: %v", truncate(line, 80), err))
			}
			continue
		}
		if seen[e] {
			continue
		}
		seen[e] = true
		res.Entries = append(res.Entries, e)
		if len(res.Entries) > max {
			return res, fmt.Errorf("entry limit %d exceeded", max)
		}
	}
	Sort(res.Entries)
	return res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func parseOne(line string, allowRegex bool) (Entry, error) {
	lower := strings.ToLower(line)
	switch {
	case strings.HasPrefix(lower, "domain-suffix:"), strings.HasPrefix(lower, "suffix:"):
		v, err := NormalizeHost(line[strings.Index(line, ":")+1:])
		return Entry{KindSuffix, v}, err
	case strings.HasPrefix(lower, "domain-keyword:"), strings.HasPrefix(lower, "keyword:"):
		return Entry{}, errors.New("keyword rules are not supported")
	case strings.HasPrefix(lower, "domain-regex:"), strings.HasPrefix(lower, "regexp:"), strings.HasPrefix(lower, "regex:"):
		if !allowRegex {
			return Entry{}, errors.New("regex disabled")
		}
		v := strings.TrimSpace(line[strings.Index(line, ":")+1:])
		if err := validateRegex(v); err != nil {
			return Entry{}, err
		}
		return Entry{KindRegex, v}, nil
	case strings.HasPrefix(lower, "full:"), strings.HasPrefix(lower, "domain-full:"):
		v, err := NormalizeHost(line[strings.Index(line, ":")+1:])
		return Entry{KindExact, v}, err
	case strings.HasPrefix(lower, "domain:"):
		// v2ray/sing-box semantics: `domain:` matches the domain and subdomains.
		v, err := NormalizeHost(line[len("domain:"):])
		return Entry{KindSuffix, v}, err
	case strings.HasPrefix(line, "*."):
		// `*.example.com` -> suffix example.com, apex included (documented).
		v, err := NormalizeHost(line[2:])
		return Entry{KindSuffix, v}, err
	case strings.HasPrefix(line, "."):
		v, err := NormalizeHost(line[1:])
		return Entry{KindSuffix, v}, err
	case strings.HasPrefix(line, "+."):
		v, err := NormalizeHost(line[2:])
		return Entry{KindSuffix, v}, err
	case strings.ContainsAny(line, "*?/"):
		return Entry{}, errors.New("unsupported wildcard form")
	default:
		// A bare domain means "this domain and everything under it" — the way a
		// person reading "unblock openai.com" expects it to work, and what the
		// community lists (v2fly, itdog) assume. Exact-host matching is the
		// explicit `full:` form.
		v, err := NormalizeHost(line)
		return Entry{KindSuffix, v}, err
	}
}

const maxRegexLen = 256

func validateRegex(v string) error {
	if len(v) == 0 || len(v) > maxRegexLen {
		return fmt.Errorf("regex length must be 1..%d", maxRegexLen)
	}
	// Go's regexp is RE2: linear time, no catastrophic backtracking.
	if _, err := regexp.Compile(v); err != nil {
		return fmt.Errorf("regex: %w", err)
	}
	return nil
}

// Sort orders entries deterministically (kind, then value).
func Sort(es []Entry) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Kind != es[j].Kind {
			return es[i].Kind < es[j].Kind
		}
		return es[i].Value < es[j].Value
	})
}

// Merge applies `(union(includes) ∪ manualAdd) − union(excludes) − manualExclude`.
func Merge(includes, excludes []Entry) []Entry {
	ex := map[Entry]bool{}
	exSuffix := map[string]bool{}
	for _, e := range excludes {
		ex[e] = true
		if e.Kind == KindSuffix {
			exSuffix[e.Value] = true
		}
	}
	out := make([]Entry, 0, len(includes))
	seen := map[Entry]bool{}
	for _, e := range includes {
		if ex[e] || seen[e] {
			continue
		}
		// An excluded suffix also removes exact entries beneath it.
		if e.Kind == KindExact && suffixHit(exSuffix, e.Value) {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	Sort(out)
	return out
}

func suffixHit(set map[string]bool, host string) bool {
	for {
		if set[host] {
			return true
		}
		i := strings.IndexByte(host, '.')
		if i < 0 {
			return false
		}
		host = host[i+1:]
	}
}

// Hash returns the canonical content hash of a normalized entry list.
func Hash(es []Entry) string {
	h := sha256.New()
	for _, e := range es {
		h.Write([]byte(e.Kind))
		h.Write([]byte{0})
		h.Write([]byte(e.Value))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Counts summarizes an entry list.
func Counts(es []Entry) map[string]int {
	c := map[string]int{"exact": 0, "suffix": 0, "regex": 0, "total": len(es)}
	for _, e := range es {
		c[string(e.Kind)]++
	}
	return c
}

// Diff reports added/removed entries between two normalized lists.
func Diff(old, new []Entry) (added, removed []Entry) {
	o := map[Entry]bool{}
	for _, e := range old {
		o[e] = true
	}
	n := map[Entry]bool{}
	for _, e := range new {
		n[e] = true
		if !o[e] {
			added = append(added, e)
		}
	}
	for _, e := range old {
		if !n[e] {
			removed = append(removed, e)
		}
	}
	Sort(added)
	Sort(removed)
	return
}
