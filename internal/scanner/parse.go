package scanner

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	yearRe    = regexp.MustCompile(`\((\d{4})\)`)
	episodeRe = regexp.MustCompile(`(?i)s(\d{1,3})[ ._-]*e(\d{1,3})`)
	altEpRe   = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,2})\b`)
	spaceRe   = regexp.MustCompile(`\s+`)
)

// cleanName normalises a raw filename fragment into a human-readable title:
// separators become spaces and runs of whitespace collapse.
func cleanName(s string) string {
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	s = spaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// sortKey derives a case-insensitive sort key from a display name.
func sortKey(name string) string {
	return strings.ToLower(name)
}

// baseNoExt returns the filename without its directory or extension.
func baseNoExt(path string) string {
	b := filepath.Base(path)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

// ParsedMovie holds the title and optional year parsed from a movie filename.
type ParsedMovie struct {
	Title string
	Year  *int32
}

// ParseMovie extracts a title and (optional) production year from a filename.
func ParseMovie(path string) ParsedMovie {
	name := baseNoExt(path)
	var year *int32
	if m := yearRe.FindStringSubmatch(name); m != nil {
		if y, err := strconv.Atoi(m[1]); err == nil {
			y32 := int32(y)
			year = &y32
		}
		name = name[:strings.Index(name, m[0])]
	}
	title := cleanName(name)
	if title == "" {
		title = cleanName(baseNoExt(path))
	}
	return ParsedMovie{Title: title, Year: year}
}

// ParsedEpisode holds metadata parsed from a TV episode file path.
type ParsedEpisode struct {
	Series  string
	Season  int32
	Episode int32
	Title   string
	OK      bool
}

// ParseEpisode extracts series name, season/episode numbers and an episode
// title from a file path. It first looks at the filename for an SxxEyy (or
// NxM) marker, deriving the series name from the text before the marker and
// falling back to the parent directory name when that text is empty.
func ParseEpisode(path string) ParsedEpisode {
	name := baseNoExt(path)
	var m []string
	var idx int
	if mm := episodeRe.FindStringSubmatchIndex(name); mm != nil {
		m = []string{name[mm[0]:mm[1]], name[mm[2]:mm[3]], name[mm[4]:mm[5]]}
		idx = mm[0]
	} else if mm := altEpRe.FindStringSubmatchIndex(name); mm != nil {
		m = []string{name[mm[0]:mm[1]], name[mm[2]:mm[3]], name[mm[4]:mm[5]]}
		idx = mm[0]
	} else {
		return ParsedEpisode{}
	}

	season, _ := strconv.Atoi(m[1])
	episode, _ := strconv.Atoi(m[2])

	series := cleanName(strings.Trim(name[:idx], " .-_"))
	if series == "" {
		series = deriveSeriesFromDir(path)
	}

	title := cleanName(strings.Trim(name[idx+len(m[0]):], " .-_"))

	return ParsedEpisode{
		Series:  series,
		Season:  int32(season),
		Episode: int32(episode),
		Title:   title,
		OK:      true,
	}
}

// deriveSeriesFromDir picks a series name from the path's directories, skipping
// directories that look like a "Season N" folder.
func deriveSeriesFromDir(path string) string {
	dir := filepath.Dir(path)
	for dir != "." && dir != string(filepath.Separator) && dir != "" {
		base := filepath.Base(dir)
		if !isSeasonDir(base) {
			return cleanName(base)
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

var seasonDirRe = regexp.MustCompile(`(?i)^(season|series|s)[ ._-]*\d+$`)

func isSeasonDir(name string) bool {
	return seasonDirRe.MatchString(strings.TrimSpace(name))
}
