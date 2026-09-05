package domainset

import (
	"regexp"
	"strings"
)

// Set is the compiled, serializable matcher artifact. Except вычитается из
// всего остального: домен, попавший в него, не считается частью набора, даже
// если его накрывает более широкий суффикс.
type Set struct {
	Exact  []string `json:"exact,omitempty"`
	Suffix []string `json:"suffix,omitempty"`
	Regex  []string `json:"regex,omitempty"`
	Except *Set     `json:"except,omitempty"`
}

// NewSet builds a Set from normalized entries.
func NewSet(es []Entry) Set {
	var s Set
	var ex Set
	for _, e := range es {
		switch e.Kind {
		case KindExact:
			s.Exact = append(s.Exact, e.Value)
		case KindSuffix:
			s.Suffix = append(s.Suffix, e.Value)
		case KindRegex:
			s.Regex = append(s.Regex, e.Value)
		case KindNotExact:
			ex.Exact = append(ex.Exact, e.Value)
		case KindNotSuffix:
			ex.Suffix = append(ex.Suffix, e.Value)
		case KindNotRegex:
			ex.Regex = append(ex.Regex, e.Value)
		}
	}
	if len(ex.Exact)+len(ex.Suffix)+len(ex.Regex) > 0 {
		s.Except = &ex
	}
	return s
}

// Matcher is the runtime form of a Set. Safe for concurrent reads.
type Matcher struct {
	exact  map[string]struct{}
	suffix map[string]struct{}
	regex  []*regexp.Regexp
	except *Matcher
}

// Compile turns a Set into a Matcher. Invalid regexes are skipped.
func (s Set) Compile() *Matcher {
	m := &Matcher{
		exact:  make(map[string]struct{}, len(s.Exact)),
		suffix: make(map[string]struct{}, len(s.Suffix)),
	}
	for _, v := range s.Exact {
		m.exact[v] = struct{}{}
	}
	for _, v := range s.Suffix {
		m.suffix[v] = struct{}{}
	}
	for _, v := range s.Regex {
		if re, err := regexp.Compile(v); err == nil {
			m.regex = append(m.regex, re)
		}
	}
	if s.Except != nil {
		m.except = s.Except.Compile()
	}
	return m
}

// Match reports whether host is covered. host must already be normalized
// (lowercase ASCII FQDN without a trailing dot).
//
// Suffix matching is label-bounded: `example.com` matches `api.example.com`
// and the apex `example.com`, but never `notexample.com`.
func (m *Matcher) Match(host string) bool {
	if m == nil || host == "" {
		return false
	}
	// Вычитание проверяется первым и бьёт любое совпадение, каким бы точным оно
	// ни было: исключение — это решение оператора, а не ещё одно правило.
	if m.except.Match(host) {
		return false
	}
	if _, ok := m.exact[host]; ok {
		return true
	}
	h := host
	for {
		if _, ok := m.suffix[h]; ok {
			return true
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	for _, re := range m.regex {
		if re.MatchString(host) {
			return true
		}
	}
	return false
}

// Specificity returns the number of matched labels for the most specific hit,
// or -1 when the host does not match. Used to resolve service overlap.
func (m *Matcher) Specificity(host string) int {
	if m == nil || host == "" {
		return -1
	}
	if m.except.Match(host) {
		return -1
	}
	if _, ok := m.exact[host]; ok {
		return 1000
	}
	h, depth := host, 0
	for {
		if _, ok := m.suffix[h]; ok {
			return 100 - depth
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
		depth++
	}
	for _, re := range m.regex {
		if re.MatchString(host) {
			return 1
		}
	}
	return -1
}

// Size returns the number of compiled rules.
func (m *Matcher) Size() int {
	if m == nil {
		return 0
	}
	return len(m.exact) + len(m.suffix) + len(m.regex) + m.except.Size()
}
