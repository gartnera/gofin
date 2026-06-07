package scanner

import (
	"os"
	"path/filepath"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// ignoreFileName is the marker file Jellyfin uses to exclude media. An empty
// .ignore excludes the whole directory; a non-empty one is parsed as a list of
// gitignore-style patterns relative to the directory that contains it.
const ignoreFileName = ".ignore"

// ignoreMatcher is a compiled .ignore file scoped to the directory it lives in.
type ignoreMatcher struct {
	root string
	gi   *gitignore.GitIgnore
}

// loadIgnore reads the .ignore file in dir, if any. It returns:
//   - skipAll=true when the file exists but holds no effective patterns, meaning
//     the entire directory (and its descendants) must be excluded;
//   - a non-nil matcher when the file lists patterns to apply;
//   - (nil, false, nil) when there is no .ignore file.
func loadIgnore(dir string) (m *ignoreMatcher, skipAll bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, ignoreFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	lines := strings.Split(string(data), "\n")
	if !hasPattern(lines) {
		// Empty (or comment/whitespace only) .ignore excludes the directory.
		return nil, true, nil
	}
	return &ignoreMatcher{root: dir, gi: gitignore.CompileIgnoreLines(lines...)}, false, nil
}

// hasPattern reports whether any line is a usable gitignore pattern (i.e. not
// blank and not a comment).
func hasPattern(lines []string) bool {
	for _, line := range lines {
		s := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		return true
	}
	return false
}

// ignored reports whether path is excluded by any active matcher. isDir governs
// matching against directory-only patterns (e.g. "specials/").
func ignored(matchers []ignoreMatcher, path string, isDir bool) bool {
	for _, m := range matchers {
		rel, ok := relUnder(m.root, path)
		if !ok {
			continue
		}
		if m.gi.MatchesPath(rel) {
			return true
		}
		if isDir && m.gi.MatchesPath(rel+"/") {
			return true
		}
	}
	return false
}

// relUnder returns path relative to root using forward slashes, reporting false
// when path is not contained within root.
func relUnder(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
