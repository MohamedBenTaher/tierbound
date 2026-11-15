package review

import (
	"strconv"
	"strings"
)

// Lines is the set of new-file (RIGHT side) line numbers a review comment
// may target in one file: the added and context lines of its hunks.
type Lines map[int]bool

// ParsePatch reads the unified-diff patch GitHub returns for one PR file.
func ParsePatch(patch string) Lines {
	lines := Lines{}
	next := 0
	for _, l := range strings.Split(patch, "\n") {
		next = step(lines, l, next)
	}
	return lines
}

func step(lines Lines, l string, next int) int {
	switch {
	case strings.HasPrefix(l, "@@"):
		return hunkStart(l)
	case next == 0 || !isNewLine(l):
		return next // before a hunk, a deletion, or "\ No newline"
	}
	lines[next] = true
	return next + 1
}

// hunkStart parses "@@ -a,b +c,d @@" into c (0 if malformed).
func hunkStart(header string) int {
	_, after, ok := strings.Cut(header, "+")
	if !ok {
		return 0
	}
	field, _, _ := strings.Cut(after, " ")
	start, _, _ := strings.Cut(field, ",")
	n, err := strconv.Atoi(start)
	if err != nil {
		return 0
	}
	return n
}

// isNewLine: an added or context line, present in the new file.
func isNewLine(l string) bool {
	return strings.HasPrefix(l, "+") || strings.HasPrefix(l, " ")
}
