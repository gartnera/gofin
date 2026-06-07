package scanner

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

// This file is a port of Jellyfin's episode path parsing (Emby.Naming/TV).
// The expression list below is transcribed, in order, from
// Emby.Naming/Common/NamingOptions.cs (EpisodeExpressions and
// MultipleEpisodeExpressions); parseEpisodePath mirrors
// Emby.Naming/TV/EpisodePathParser.cs.
//
// We deliberately use github.com/dlclark/regexp2 rather than the standard
// library's regexp: the upstream expressions rely on negative lookaheads
// (e.g. (?![Ss][0-9]+[Ee][0-9]+)) and .NET-style named groups, neither of
// which RE2 supports. regexp2 implements .NET regex semantics, so the
// patterns can be ported verbatim.
//
// PERFORMANCE DIVERGENCE FROM UPSTREAM (see fillAdditional): regexp2 is a
// backtracking engine and, unlike .NET's compiled regex, is *slow* — a single
// multi-episode expression run against a long, hyphen-laden absolute path costs
// hundreds of microseconds. Upstream's EpisodePathParser.FillAdditional runs
// all of MultipleEpisodeExpressions against the full path on every file, which
// added ~3.4ms/episode here (≈95s to index 10k episodes). gofin diverges from
// the literal port in two behaviour-preserving ways, both justified inline at
// fillAdditional and proven equivalent to the verbatim algorithm by
// TestEpisodeParseMatchesUpstream. The divergence is purely in *how much work*
// is done, never in the parse result.

// episodeExpression is one ported Jellyfin episode-naming rule.
type episodeExpression struct {
	re *regexp2.Regexp
	// named matches Jellyfin's IsNamed: the season/episode come from the
	// (?<seasonnumber>)/(?<epnumber>) groups rather than positional groups.
	named bool
	// byDate matches Jellyfin's IsByDate: the whole match is an air date.
	byDate      bool
	dateFormats []string
}

func expr(pattern string) episodeExpression {
	return episodeExpression{re: regexp2.MustCompile(pattern, regexp2.IgnoreCase), named: true}
}

func unnamedExpr(pattern string) episodeExpression {
	return episodeExpression{re: regexp2.MustCompile(pattern, regexp2.IgnoreCase)}
}

func dateExpr(pattern string, formats ...string) episodeExpression {
	return episodeExpression{re: regexp2.MustCompile(pattern, regexp2.IgnoreCase), byDate: true, dateFormats: formats}
}

// episodeExpressions is the ordered list of episode rules, tried in turn until
// one matches (Emby.Naming/Common/NamingOptions.cs#EpisodeExpressions).
var episodeExpressions = []episodeExpression{
	// foo.s01.e01, foo.s01_e01, S01E02 foo, S01 - E02
	expr(`.*(\\|\/)(?<seriesname>((?![Ss]([0-9]+)[][ ._-]*[Ee]([0-9]+))[^\\\/])*)?[Ss](?<seasonnumber>[0-9]+)[][ ._-]*[Ee](?<epnumber>[0-9]+)([^\\/]*)$`),
	// foo.ep01, foo.EP_01
	unnamedExpr(`[\._ -]()[Ee][Pp]_?([0-9]+)([^\\/]*)$`),
	// foo.E01., foo.e01.
	unnamedExpr(`[^\\/]*?()\.?[Ee]([0-9]+)\.([^\\/]*)$`),
	// 2009.12.31, 2009-12-31, 2009_12_31
	dateExpr(`(?<year>[0-9]{4})[._ -](?<month>[0-9]{2})[._ -](?<day>[0-9]{2})`,
		"yyyy.MM.dd", "yyyy-MM-dd", "yyyy_MM_dd", "yyyy MM dd"),
	// 31.12.2009, 31-12-2009, 31_12_2009
	dateExpr(`(?<day>[0-9]{2})[._ -](?<month>[0-9]{2})[._ -](?<year>[0-9]{4})`,
		"dd.MM.yyyy", "dd-MM-yyyy", "dd_MM_yyyy", "dd MM yyyy"),
	// "Series Season X Episode X - Title", "Series S03 E09", "s3 e9 - Title"
	expr(`.*[\\\/]((?<seriesname>[^\\/]+?)\s)?[Ss](?:eason)?\s*(?<seasonnumber>[0-9]+)\s+[Ee](?:pisode)?\s*(?<epnumber>[0-9]+).*$`),
	// "Foo Bar 889"
	expr(`.*[\\\/](?![Ee]pisode)(?<seriesname>[\w\s]+?)\s(?<epnumber>[0-9]{1,4})(-(?<endingepnumber>[0-9]{2,4}))*[^\\\/x]*$`),
	// 01x02, with optional absolute-number suffix (1080p safe via [a-i])
	unnamedExpr(`[\\\/\._ \[\(-]([0-9]+)x([0-9]+(?:(?:[a-i]|\.[1-9])(?![0-9]))?)([^\\\/]*)$`),
	// "[bar] Foo - 1 [baz]"
	expr(`.*[\\\/]?.*?(\[.*?\])+.*?(?<seriesname>[-\w\s]+?)[\s_]*-[\s_]*(?<epnumber>[0-9]+).*$`),
	// "Name - 101.mkv", "Name - 101 [720p].mkv" (anime absolute, hyphen delimited)
	expr(`.*[\\\/](?<seriesname>[^\\\/]+?)[\s_]+-[\s_]+(?<epnumber>[0-9]+)[\s_]*(?:\[.*?\]|\(.*?\))*[\s_]*(?:\.\w+)?$`),
	// /server/anything_102.mp4 (optimistic season+episode embedded in name)
	expr(`[\\/._ -](?<seriesname>(?![0-9]+[0-9][0-9])([^\\\/_])*)[\\\/._ -](?<seasonnumber>[0-9]+)(?<epnumber>[0-9][0-9](?:(?:[a-i]|\.[1-9])(?![0-9]))?)([._ -][^\\\/]*)$`),
	// part 1, pt2
	unnamedExpr(`[\/._ -]p(?:ar)?t[_. -]()([ivx]+|[0-9]+)([._ -][^\/]*)$`),
	// "Episode 16", "Episode 16 - Title"
	expr(`[Ee]pisode (?<epnumber>[0-9]+)(-(?<endingepnumber>[0-9]+))?[^\\\/]*$`),
	// 1x02
	expr(`.*(\\|\/)[sS]?(?<seasonnumber>[0-9]+)[xX](?<epnumber>[0-9]+)[^\\\/]*$`),
	// S01E02
	expr(`.*(\\|\/)[sS](?<seasonnumber>[0-9]+)[x,X]?[eE](?<epnumber>[0-9]+)[^\\\/]*$`),
	// seriesname + 1x02
	expr(`.*(\\|\/)(?<seriesname>((?![sS]?[0-9]{1,4}[xX][0-9]{1,3})[^\\\/])*)?([sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]+))[^\\\/]*$`),
	// seriesname + S01E02
	expr(`.*(\\|\/)(?<seriesname>[^\\\/]*)[sS](?<seasonnumber>[0-9]{1,4})[xX\.]?[eE](?<epnumber>[0-9]+)[^\\\/]*$`),
	// "01.avi"
	expr(`.*[\\\/](?<epnumber>[0-9]+)(-(?<endingepnumber>[0-9]+))*\.\w+$`),
	// "1-12 episode title"
	unnamedExpr(`([0-9]+)-([0-9]+)`),
	// "01 - blah.avi", "01-blah.avi"
	expr(`.*(\\|\/)(?<epnumber>[0-9]{1,3})(-(?<endingepnumber>[0-9]{2,3}))*\s?-\s?[^\\\/]*$`),
	// "01.blah.avi"
	expr(`.*(\\|\/)(?<epnumber>[0-9]{1,3})(-(?<endingepnumber>[0-9]{2,3}))*\.[^\\\/]+$`),
	// "blah - 01.avi", "blah 2 - 01 - blah"
	expr(`.*[\\\/][^\\\/]* - (?<epnumber>[0-9]{1,3})(-(?<endingepnumber>[0-9]{2,3}))*[^\\\/]*$`),
	// "Season 1/01 episode title.avi"
	expr(`[Ss]eason[\._ ](?<seasonnumber>[0-9]+)[\\\/](?<epnumber>[0-9]{1,3})([^\\\/]*)$`),
	// series + season only: "the show/season 1", "the show/s01"
	expr(`(.*(\\|\/))*(?<seriesname>.+)\/[Ss](eason)?[\. _\-]*(?<seasonnumber>[0-9]+)`),
	// series + season only: "the show S01", "the show season 1"
	expr(`(.*(\\|\/))*(?<seriesname>.+)[\. _\-]+[sS](eason)?[\. _\-]*(?<seasonnumber>[0-9]+)`),
	// Anime: "[Group] Series Name [04][BDRIP]"
	expr(`(?:\[(?:[^\]]+)\]\s*)?(?<seriesname>\[[^\]]+\]|[^[\]]+)\s*\[(?<epnumber>[0-9]+)\]`),
}

// multipleEpisodeExpressions detect multi-episode files (S01E01-E02, 1x01x02,
// …). They are run during the FillAdditional pass to recover an ending episode
// number (Emby.Naming/Common/NamingOptions.cs#MultipleEpisodeExpressions).
var multipleEpisodeExpressions = func() []episodeExpression {
	patterns := []string{
		`.*(\\|\/)[sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3})((-| - )[0-9]{1,4}[eExX](?<endingepnumber>[0-9]{1,3}))+[^\\\/]*$`,
		`.*(\\|\/)[sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3})((-| - )[0-9]{1,4}[xX][eE](?<endingepnumber>[0-9]{1,3}))+[^\\\/]*$`,
		`.*(\\|\/)[sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3})((-| - )?[xXeE](?<endingepnumber>[0-9]{1,3}))+[^\\\/]*$`,
		`.*(\\|\/)[sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3})(-[xE]?[eE]?(?<endingepnumber>[0-9]{1,3}))+[^\\\/]*$`,
		`.*(\\|\/)(?<seriesname>((?![sS]?[0-9]{1,4}[xX][0-9]{1,3})[^\\\/])*)?([sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3}))((-| - )[0-9]{1,4}[xXeE](?<endingepnumber>[0-9]{1,3}))+[^\\\/]*$`,
		`.*(\\|\/)(?<seriesname>((?![sS]?[0-9]{1,4}[xX][0-9]{1,3})[^\\\/])*)?([sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3}))((-| - )[0-9]{1,4}[xX][eE](?<endingepnumber>[0-9]{1,3}))+[^\\\/]*$`,
		`.*(\\|\/)(?<seriesname>((?![sS]?[0-9]{1,4}[xX][0-9]{1,3})[^\\\/])*)?([sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3}))((-| - )?[xXeE](?<endingepnumber>[0-9]{1,3}))+[^\\\/]*$`,
		`.*(\\|\/)(?<seriesname>((?![sS]?[0-9]{1,4}[xX][0-9]{1,3})[^\\\/])*)?([sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3}))(-[xX]?[eE]?(?<endingepnumber>[0-9]{1,3}))+[^\\\/]*$`,
		`.*(\\|\/)(?<seriesname>[^\\\/]*)[sS](?<seasonnumber>[0-9]{1,4})[xX\.]?[eE](?<epnumber>[0-9]{1,3})((-| - )?[xXeE](?<endingepnumber>[0-9]{1,3}))+[^\\\/]*$`,
		`.*(\\|\/)(?<seriesname>[^\\\/]*)[sS](?<seasonnumber>[0-9]{1,4})[xX\.]?[eE](?<epnumber>[0-9]{1,3})(-[xX]?[eE]?(?<endingepnumber>[0-9]{1,3}))+[^\\\/]*$`,
	}
	out := make([]episodeExpression, len(patterns))
	for i, p := range patterns {
		out[i] = expr(p)
	}
	return out
}()

// episodeResult mirrors Jellyfin's EpisodePathParserResult.
type episodeResult struct {
	SeriesName          string
	SeasonNumber        *int
	EpisodeNumber       *int
	EndingEpisodeNumber *int
	Year, Month, Day    int
	IsByDate            bool
	Success             bool
}

// parseEpisodePath parses season/episode/series information from a path,
// porting EpisodePathParser.Parse. Directories get a synthetic ".mp4" suffix so
// that extension-anchored expressions still apply, exactly as upstream does.
func parseEpisodePath(path string, isDirectory bool) episodeResult {
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

	fillAdditional(name, result)
	if result.SeriesName != "" {
		result.SeriesName = strings.Trim(strings.TrimSpace(result.SeriesName), "_.-")
		result.SeriesName = strings.TrimSpace(result.SeriesName)
	}
	return *result
}

// matchExpr applies a single expression to name, porting the private
// EpisodePathParser.Parse(name, expression).
func matchExpr(name string, e episodeExpression) episodeResult {
	var res episodeResult
	candidate := name
	if e.byDate {
		// Hack to handle wmc naming (underscores as date separators).
		candidate = strings.ReplaceAll(candidate, "_", "-")
	}

	m, err := e.re.FindStringMatch(candidate)
	if err != nil || m == nil {
		return res
	}
	// Mirror upstream's `match.Groups.Count >= 3` guard.
	if m.GroupCount() < 3 {
		return res
	}

	switch {
	case e.byDate:
		whole := m.String()
		for _, f := range e.dateFormats {
			if t, err := time.Parse(goDateLayout(f), whole); err == nil {
				res.Year, res.Month, res.Day = t.Year(), int(t.Month()), t.Day()
				break
			}
		}
		// Upstream marks date matches successful regardless of parse outcome.
		res.Success = true
	case e.named:
		if n, ok := groupInt(m.GroupByName("seasonnumber")); ok {
			res.SeasonNumber = &n
		}
		if n, ok := groupInt(m.GroupByName("epnumber")); ok {
			res.EpisodeNumber = &n
		}
		if g := m.GroupByName("endingepnumber"); g != nil && len(g.Captures) > 0 {
			// Only honour the ending number when it is not immediately followed
			// by a digit or a resolution marker (i/p), so "s09e14-1080p" is not
			// read as episodes 14 through 108.
			runes := []rune(candidate)
			next := g.Index + g.Length
			if next >= len(runes) || !strings.ContainsRune("0123456789iIpP", runes[next]) {
				if n, ok := groupInt(g); ok {
					res.EndingEpisodeNumber = &n
				}
			}
		}
		res.SeriesName = groupString(m.GroupByName("seriesname"))
		res.Success = res.EpisodeNumber != nil
	default:
		if n, ok := groupInt(m.GroupByNumber(1)); ok {
			res.SeasonNumber = &n
		}
		if n, ok := groupInt(m.GroupByNumber(2)); ok {
			res.EpisodeNumber = &n
		}
		res.Success = res.EpisodeNumber != nil
	}

	// Invalidate implausible season numbers (200–1927 or >2500) so resolutions
	// like "Special (1920x1080)" are not read as season 1920 episode 1080.
	if res.SeasonNumber != nil {
		s := *res.SeasonNumber
		if (s >= 200 && s < 1928) || s > 2500 {
			res.Success = false
		}
	}

	res.IsByDate = e.byDate
	return res
}

// fillAdditional ports EpisodePathParser.FillAdditional: it recovers a missing
// series name from the named expressions and an ending episode number from the
// multi-episode expressions.
//
// It diverges from the verbatim upstream port in two performance-only ways,
// each behaviour-preserving and proven so by TestEpisodeParseMatchesUpstream
// (which compares this against fillAdditionalReference, a literal copy of the
// upstream algorithm, over a corpus of single/multi/edge-case names):
//
//  1. GATE. Upstream always runs MultipleEpisodeExpressions. Every one of those
//     patterns can only match a name that contains a range marker, so we skip
//     the whole pass for names that provably can't encode one (couldBeMultiEpisode).
//     This is the common case — a lone "SxxExx" or a title hyphen like
//     "Series - Title" — and the gate is a verified necessary condition, so a
//     skipped name could never have matched.
//  2. BASENAME SCOPING. Every MultipleEpisodeExpression is tail-anchored within
//     the final path segment ([^\\/]*$ with no slash-crossing groups), so we run
//     them against just that segment instead of the full path. The leading
//     `.*(\\|\/)` then has a single short string to chew rather than a long
//     directory chain, eliminating the pathological backtracking; the captured
//     groups (all within the last segment) are unchanged. The named expressions
//     used for series-name recovery can legitimately read a *parent directory*,
//     so those still see the full path.
func fillAdditional(name string, info *episodeResult) {
	// Each candidate carries the input it should be matched against (full path
	// for series-name recovery; basename for the multi-episode pass — see the
	// divergence note above).
	type candidate struct {
		e     episodeExpression
		input string
	}
	var cands []candidate
	if info.SeriesName == "" {
		for _, e := range episodeExpressions {
			if e.named {
				cands = append(cands, candidate{e, name})
			}
		}
	}
	// Multi-episode expressions require a range marker to match at all, so skip
	// them entirely for names that provably can't encode one (the common,
	// single-episode case).
	if info.EndingEpisodeNumber == nil && couldBeMultiEpisode(name) {
		base := "/" + name[strings.LastIndexByte(name, '/')+1:]
		for _, e := range multipleEpisodeExpressions {
			cands = append(cands, candidate{e, base})
		}
	}
	if len(cands) == 0 {
		return
	}

	for _, c := range cands {
		r := matchExpr(c.input, c.e)
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

// multiEpisodeHint is a necessary condition for any multipleEpisodeExpression to
// match — verified against all of them. A range always appears as one of:
//
//   - a hyphen (optionally spaced) followed by an episode number, with up to
//     two optional marker letters in between: "-E02", " - 02", "-x02", "-1E02"
//     (the (-| - )[0-9]…[exEX] and (-[xX]?[eE]?…) tail forms); or
//   - two adjacent marker+number tokens with no hyphen: "E01E02", "1x01x02"
//     (the ((-| - )?[xXeE]…)+ tail forms).
//
// Crucially this does NOT fire on a lone "SxxExx" token or a title hyphen such
// as "Series - Title", so the overwhelmingly common single-episode file skips
// the (expensive, pure-backtracking) multi-episode pass entirely.
var multiEpisodeHint = regexp.MustCompile(`(?i)(-\s*[ex]{0,2}[0-9])|([ex][0-9]+[ex][0-9])`)

// couldBeMultiEpisode reports whether name's final path segment could plausibly
// encode an episode range, gating the expensive multi-episode regex pass.
func couldBeMultiEpisode(name string) bool {
	base := name[strings.LastIndexByte(name, '/')+1:]
	return multiEpisodeHint.MatchString(base)
}

// groupInt parses an integer from a regexp2 group, reporting whether the group
// participated in the match and held a number.
func groupInt(g *regexp2.Group) (int, bool) {
	if g == nil || len(g.Captures) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(g.String()))
	if err != nil {
		return 0, false
	}
	return n, true
}

// groupString returns a group's text, or "" when it did not participate.
func groupString(g *regexp2.Group) string {
	if g == nil || len(g.Captures) == 0 {
		return ""
	}
	return g.String()
}

// goDateLayout converts a .NET date format (yyyy/MM/dd) into a Go time layout.
func goDateLayout(f string) string {
	return strings.NewReplacer("yyyy", "2006", "MM", "01", "dd", "02").Replace(f)
}
