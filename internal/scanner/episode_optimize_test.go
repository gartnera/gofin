package scanner

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fillAdditionalReference is a verbatim copy of Jellyfin's
// EpisodePathParser.FillAdditional behaviour as gofin originally ported it,
// before the performance divergence documented on fillAdditional: it appends
// ALL multipleEpisodeExpressions unconditionally and matches every expression
// against the full path. It exists solely as the oracle for
// TestEpisodeParseMatchesUpstream — if the optimized fillAdditional ever
// produces a different result than this, the test fails.
func fillAdditionalReference(name string, info *episodeResult) {
	var exprs []episodeExpression
	if info.SeriesName == "" {
		for _, e := range episodeExpressions {
			if e.named {
				exprs = append(exprs, e)
			}
		}
	}
	exprs = append(exprs, multipleEpisodeExpressions...)

	for _, e := range exprs {
		r := matchExpr(name, e)
		if !r.Success {
			continue
		}
		if info.SeriesName == "" {
			info.SeriesName = r.SeriesName
		}
		if info.EndingEpisodeNumber == nil && info.EpisodeNumber != nil {
			info.EndingEpisodeNumber = r.EndingEpisodeNumber
		}
		if info.SeriesName != "" && (info.EpisodeNumber == nil || info.EndingEpisodeNumber != nil) {
			break
		}
	}
}

// parseEpisodePathReference mirrors parseEpisodePath but uses the verbatim
// upstream fillAdditional, giving a reference parse to compare against.
func parseEpisodePathReference(path string, isDirectory bool) episodeResult {
	name := filepath.ToSlash(path)
	if isDirectory {
		name += ".mp4"
	}
	var result *episodeResult
	for _, e := range episodeExpressions {
		if r := matchExpr(name, e); r.Success {
			rr := r
			result = &rr
			break
		}
	}
	if result == nil {
		return episodeResult{}
	}
	fillAdditionalReference(name, result)
	if result.SeriesName != "" {
		result.SeriesName = strings.Trim(strings.TrimSpace(result.SeriesName), "_.-")
		result.SeriesName = strings.TrimSpace(result.SeriesName)
	}
	return *result
}

// episodeCorpus is a broad set of episode paths spanning every
// multipleEpisodeExpression form, single episodes that merely *look* range-like
// (title hyphens, the SxxExx token itself, trailing resolutions), and the
// flat/anime/date/season-only shapes the first pass handles.
var episodeCorpus = []string{
	// Plain single episodes (must NOT be read as ranges).
	"/tv/Show/Season 01/Show - S01E01.mkv",
	"/tv/Show/Season 01/Show - S01E01 - Pilot.mkv",
	"/tv/The X-Files/Season 01/The X-Files - S01E01 - Pilot.mkv",
	"/tv/Spider-Man/Season 1/Spider-Man S01E01.mkv",
	"/tv/Show/Season 1/Show 1x01.mkv",
	"/tv/Show/Season 1/Show 1x01 - Title.mkv",
	"/tv/series-s09e14-1080p.mkv",
	"/tv/Show/Season 09/Show - S09E14 1080p x264.mkv",
	"/tv/Pre-Release Show/Season 02/Pre-Release Show - S02E05.mkv",
	// Hyphen-delimited ranges, with/without markers and series names.
	"/tv/Show/Season 1/Show - S01E01-E02.mkv",
	"/tv/Show/Season 1/Show - S01E01-E03.mkv",
	"/tv/Show/Season 1/Show - S01E01-02.mkv",
	"/tv/Show/Season 1/Show S01E01 - E02.mkv",
	"/tv/Show/Season 1/Show - S01E01-E02 - Two Titles.mkv",
	"/tv/Chuck/Chuck - S01E01-E02.mkv",
	"/tv/Show/Season 1/01x01-02.mkv",
	"/tv/Show/Season 1/Show 1x01-x02.mkv",
	// Concatenated ranges (no hyphen).
	"/tv/Show/Show 1x01x02.mkv",
	"/tv/Show/Show 1x01x02x03.mkv",
	"/tv/Show/Season 1/Show S01E01E02.mkv",
	"/tv/Show/Season 1/Show - S01E01xE02.mkv",
	// Flat shows / anime absolute numbering / bracketed groups.
	"/tv/Firefly/Firefly S01E01.mkv",
	"/tv/Anime/Anime - 101.mkv",
	"/tv/Anime/[Group] Anime - 05 [BD].mkv",
	"/tv/Anime/[Group] Anime Name [04][BDRIP].mkv",
	"/tv/Show/Season 1/Episode 16.mkv",
	"/tv/Show/Season 1/Episode 16 - Title.mkv",
	"/tv/Foo Bar 889.mkv",
	"/tv/Show/Season 1/01 - blah.avi",
	"/tv/Show/Season 1/01.blah.avi",
	"/tv/Show/Season 1/blah - 01.avi",
	// Date-based and season-only.
	"/tv/News/News 2009.12.31.mkv",
	"/tv/News/2009-12-31 Episode.mkv",
	"/tv/The Show/Season 1",
	"/tv/The Show/S01",
	"/tv/Nonsense/just-some-text.mkv",
}

// TestEpisodeParseMatchesUpstream proves the optimized fillAdditional (gate +
// basename scoping) is behaviour-identical to the verbatim Jellyfin port across
// a broad corpus, as both files and directories, in plain and Windows path
// forms.
func TestEpisodeParseMatchesUpstream(t *testing.T) {
	for _, p := range episodeCorpus {
		variants := []string{p, strings.ReplaceAll(p, "/", `\`)}
		for _, v := range variants {
			for _, isDir := range []bool{false, true} {
				got := parseEpisodePath(v, isDir)
				want := parseEpisodePathReference(v, isDir)
				if !reflect.DeepEqual(got, want) {
					t.Errorf("parseEpisodePath(%q, dir=%v) diverged from upstream:\n got  %+v\n want %+v",
						v, isDir, got, want)
				}
			}
		}
	}
}

// TestCouldBeMultiEpisodeIsNecessary asserts the gate never excludes a name
// that a multipleEpisodeExpression would actually match — a false negative
// would silently drop an episode range. For every corpus name where any
// multi-expression matches (against the basename, as upstream tail-anchors
// them), couldBeMultiEpisode must report true.
func TestCouldBeMultiEpisodeIsNecessary(t *testing.T) {
	for _, p := range episodeCorpus {
		for _, v := range []string{p, strings.ReplaceAll(p, "/", `\`)} {
			name := filepath.ToSlash(v)
			base := "/" + name[strings.LastIndexByte(name, '/')+1:]
			matched := false
			for _, e := range multipleEpisodeExpressions {
				if matchExpr(base, e).Success {
					matched = true
					break
				}
			}
			if matched && !couldBeMultiEpisode(name) {
				t.Errorf("couldBeMultiEpisode(%q)=false but a multi-expression matches it", v)
			}
		}
	}
}

// TestCouldBeMultiEpisodeGatesSingles documents the win: representative
// single-episode names — including ones with title or name hyphens that the
// naive "contains a hyphen" check would have failed to gate — are skipped.
//
// The gate is intentionally conservative: a hyphen *followed by* a number, as
// in "...-1080p", still trips it (a hyphenated resolution looks like the start
// of a "-NN" range). That's a harmless false positive — the full pass runs and
// correctly finds no range (proven by TestEpisodeParseMatchesUpstream) — so
// such names are deliberately excluded from this list.
func TestCouldBeMultiEpisodeGatesSingles(t *testing.T) {
	singles := []string{
		"Show - S01E01.mkv",
		"Show - S01E01 - Pilot.mkv",
		"The X-Files - S01E01 - Pilot.mkv",
		"Spider-Man S01E01.mkv",
		"Show 1x01.mkv",
		"Show - S09E14 1080p x264.mkv",
	}
	for _, s := range singles {
		if couldBeMultiEpisode("/tv/Show/" + s) {
			t.Errorf("couldBeMultiEpisode unexpectedly true for single episode %q", s)
		}
	}
}
