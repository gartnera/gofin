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
	// trailingYearRe matches a production year at the end of a series name,
	// either parenthesised "(2008)" or bare "2008".
	trailingYearRe = regexp.MustCompile(`\s*\(?(?:19|20)\d{2}\)?$`)
	// qualityTagRe matches the first quality/source/codec token in a name and
	// the separator before it. The token list is ported from Jellyfin's
	// CleanStrings (Emby.Naming/Common/NamingOptions.cs); everything from the
	// match onward is release metadata, not part of the title.
	qualityTagRe = regexp.MustCompile(`(?i)(?:^|[ ._,()\[\]-])(?:3d|sbs|tab|hsbs|htab|mvc|hdr|hdc|uhd|ultrahd|4k|ac3|dts|custom|dc|divx|divx5|dsr|dsrip|dutch|dvd|dvdrip|dvdscr|dvdscreener|screener|dvdivx|cam|fragment|fs|hdtv|hdrip|hdtvrip|internal|limited|multi|subs|ntsc|ogg|ogm|pal|pdtv|proper|repack|rerip|retail|cd[1-9]|r5|bd5|bd|se|svcd|swedish|german|nfofix|unrated|ws|telesync|ts|telecine|tc|brrip|bdrip|480p|480i|576p|576i|720p|720i|1080p|1080i|2160p|hrhd|hrhdtv|hddvd|bluray|blu-ray|x264|x265|h264|h265|xvid|xvidvd|xxx|aac)(?:$|[ ._,()\[\]-])`)
)

// stripQualityTags truncates a name at the first release-metadata token (e.g.
// "1080p", "x264", "HDTV"), returning only the leading title portion.
func stripQualityTags(s string) string {
	if loc := qualityTagRe.FindStringIndex(s); loc != nil {
		return strings.TrimRight(s[:loc[0]], " ._,()[]-")
	}
	return s
}

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
	Series     string
	Season     int32
	Episode    int32
	EndEpisode *int32
	Title      string
	OK         bool
}

// ParseEpisode extracts series name, season/episode numbers and an episode
// title from a file path using the Jellyfin-ported rules in episode.go. The
// series name is derived from the matching expression (typically the text or
// parent directory before the season/episode marker) and normalised so the
// same show resolves to one Series even when its seasons live in separate
// directories. Episodes without an explicit season (flat shows, anime absolute
// numbering) are filed under season 1.
func ParseEpisode(path string) ParsedEpisode {
	r := parseEpisodePath(path, false)
	// Date-based episodes carry no season/episode number and have no home in
	// the current index schema, so leave them unindexed for now.
	if !r.Success || r.IsByDate || r.EpisodeNumber == nil {
		return ParsedEpisode{}
	}

	season := int32(1)
	if r.SeasonNumber != nil {
		season = int32(*r.SeasonNumber)
	}

	series := normalizeSeriesName(r.SeriesName)
	if series == "" {
		series = deriveSeriesFromDir(path)
	}

	parsed := ParsedEpisode{
		Series:  series,
		Season:  season,
		Episode: int32(*r.EpisodeNumber),
		Title:   extractTitle(path),
		OK:      true,
	}
	if r.EndingEpisodeNumber != nil && *r.EndingEpisodeNumber >= *r.EpisodeNumber {
		end := int32(*r.EndingEpisodeNumber)
		parsed.EndEpisode = &end
	}
	return parsed
}

// extractTitle recovers a human-readable episode title from the trailing text
// after an SxxEyy or NxM marker in the filename. The Jellyfin parser does not
// surface episode titles (those come from metadata providers), so this keeps a
// best-effort title for the common, clearly-delimited cases.
func extractTitle(path string) string {
	base := baseNoExt(path)
	for _, re := range []*regexp.Regexp{episodeRe, altEpRe} {
		if loc := re.FindStringIndex(base); loc != nil {
			rest := base[loc[1]:]
			// Strip release metadata ("1080p", "x264", …) and a multi-episode
			// continuation ("-E02") so a file like "Show - S01E01-E02 1080p"
			// yields no spurious title.
			rest = stripQualityTags(rest)
			rest = epContinuationRe.ReplaceAllString(rest, "")
			return cleanName(strings.Trim(rest, " .-_"))
		}
	}
	return ""
}

// epContinuationRe matches an episode-range continuation immediately following
// the primary SxxEyy/NxM marker, such as "-E02" or "x03".
var epContinuationRe = regexp.MustCompile(`(?i)^[ ._-]*(?:[-x]\s*)?e?\d+`)

// normalizeSeriesName cleans a raw series-name fragment and strips a trailing
// production year so that, e.g., "Breaking Bad (2008)" and "Breaking Bad"
// resolve to the same Series row regardless of folder layout.
func normalizeSeriesName(s string) string {
	s = cleanName(s)
	s = stripQualityTags(s)
	if m := trailingYearRe.FindStringIndex(s); m != nil {
		s = strings.TrimSpace(s[:m[0]])
	}
	return strings.TrimSpace(s)
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
