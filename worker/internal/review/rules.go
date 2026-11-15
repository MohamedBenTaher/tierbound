package review

import (
	"regexp"
	"strconv"
	"strings"
)

// minQuote keeps a "quote" from being a word that happens to appear
// anywhere ("the", "must").
const minQuote = 20

var (
	numbered    = regexp.MustCompile(`^\s*(\d+)\.\s`)
	slugDrop    = regexp.MustCompile(`[^a-z0-9 -]`)
	anchorIndex = regexp.MustCompile(`^(.*)-(\d+)$`)
)

// normalize removes markdown emphasis and code ticks and collapses
// whitespace, so a quote is verbatim modulo markup and line wrapping.
func normalize(s string) string {
	s = strings.NewReplacer("**", "", "`", "", "__", "").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// slug is GitHub's heading anchor: lower case, punctuation dropped, spaces
// to hyphens.
func slug(heading string) string {
	s := slugDrop.ReplaceAllString(strings.ToLower(strings.TrimSpace(heading)), "")
	return strings.ReplaceAll(s, " ", "-")
}

// span returns the text an anchor names in a rule file. Markdown anchors
// are "<heading-slug prefix>[-<n>]": the section whose heading slug starts
// with the prefix, narrowed to its n-th numbered item when n is given.
// Other files (lint configs) have no sections: the anchor must occur in the
// file, and the span is the whole file.
func span(source, text, anchor string) (string, bool) {
	if !strings.HasSuffix(source, ".md") {
		return text, anchor != "" && strings.Contains(text, anchor)
	}
	prefix, item := anchor, 0
	if m := anchorIndex.FindStringSubmatch(anchor); m != nil {
		prefix, item = m[1], atoi(m[2])
	}
	sec, ok := section(text, prefix)
	if !ok || item == 0 {
		return sec, ok
	}
	return numberedItem(sec, item)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func headingMatches(line, prefix string) bool {
	if !strings.HasPrefix(line, "#") || prefix == "" {
		return false
	}
	s := slug(strings.TrimLeft(line, "#"))
	return s == prefix || strings.HasPrefix(s, prefix+"-")
}

func section(text, prefix string) (string, bool) {
	var out []string
	in := false
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "#") && in {
			break
		}
		in = in || headingMatches(l, prefix)
		if in {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n"), in
}

func numberedItem(sec string, n int) (string, bool) {
	var out []string
	in := false
	for _, l := range strings.Split(sec, "\n") {
		if m := numbered.FindStringSubmatch(l); m != nil {
			if in {
				break
			}
			in = atoi(m[1]) == n
		}
		if in {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n"), in
}
