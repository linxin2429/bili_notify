package state

import (
	"strings"
	"unicode"
)

const (
	defaultContentLimit = 20
	maxContentLimit     = 100
)

func normalizePage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultContentLimit
	}
	if limit > maxContentLimit {
		limit = maxContentLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// foldSearch lowercases ASCII letters for case-insensitive Latin match; CJK is unchanged.
func foldSearch(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r <= unicode.MaxASCII {
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
