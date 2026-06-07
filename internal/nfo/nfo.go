// Package nfo reads local Kodi/Jellyfin ".nfo" sidecar metadata files. These
// XML files sit alongside (or in a parent directory of) a media file and carry
// human-curated metadata — overview, genres, cast, ratings — that the scanner
// merges over what it can infer from the filename.
//
// See https://jellyfin.org/docs/general/server/metadata/nfo for the format.
//
// NFO files are never treated as indexable media in their own right: the
// scanner and watcher only match audio/video extensions, so a ".nfo" is read
// solely as a side effect of indexing the media file it describes.
package nfo

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/text/encoding/htmlindex"
)

// Person is a cast or crew member parsed from an NFO file. Type holds a
// Jellyfin person kind ("Actor", "Director", "Writer", ...).
type Person struct {
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
	Type string `json:"type"`
}

// Info is the metadata distilled from an NFO file. Zero/empty fields mean the
// NFO did not supply that value and the scanner should keep what it already
// has.
type Info struct {
	Title           string
	Overview        string
	Tagline         string
	Year            *int32
	PremiereDate    *time.Time
	CommunityRating *float32
	OfficialRating  string
	Genres          []string
	Studios         []string
	People          []Person
	// Season and Episode are populated for episode NFOs when present.
	Season  *int32
	Episode *int32
}

// rawNFO mirrors the union of fields used across the Kodi NFO root elements
// (<movie>, <episodedetails>, <tvshow>, <season>). encoding/xml matches child
// elements by tag regardless of the root element's name, so a single struct
// decodes every variant.
type rawNFO struct {
	Title         string   `xml:"title"`
	OriginalTitle string   `xml:"originaltitle"`
	Plot          string   `xml:"plot"`
	Outline       string   `xml:"outline"`
	Tagline       string   `xml:"tagline"`
	Year          *int32   `xml:"year"`
	Premiered     string   `xml:"premiered"`
	Aired         string   `xml:"aired"`
	ReleaseDate   string   `xml:"releasedate"`
	Rating        *float32 `xml:"rating"`
	MPAA          string   `xml:"mpaa"`
	Genres        []string `xml:"genre"`
	Studios       []string `xml:"studio"`
	Season        *int32   `xml:"season"`
	Episode       *int32   `xml:"episode"`
	Directors     []string `xml:"director"`
	Writers       []string `xml:"writer"`
	Credits       []string `xml:"credits"`
	Actors        []struct {
		Name  string `xml:"name"`
		Role  string `xml:"role"`
		Order *int   `xml:"order"`
	} `xml:"actor"`
	// Ratings is the Kodi v17+ nested form: <ratings><rating default="true">
	// <value>8.5</value></rating></ratings>.
	Ratings struct {
		Rating []struct {
			Default bool    `xml:"default,attr"`
			Value   float32 `xml:"value"`
		} `xml:"rating"`
	} `xml:"ratings"`
}

// Parse reads and decodes a single NFO file. A non-UTF-8 charset declared in
// the XML header (older libraries are often Windows-1252/ISO-8859-1) is decoded
// transparently, matching Kodi/Jellyfin tolerance.
func Parse(path string) (*Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.CharsetReader = charsetReader
	var raw rawNFO
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	return raw.toInfo(), nil
}

// charsetReader resolves a declared (non-UTF-8) XML encoding label to a decoder
// that re-encodes its input as UTF-8. encoding/xml invokes this only when the
// header declares an encoding other than UTF-8.
func charsetReader(label string, input io.Reader) (io.Reader, error) {
	enc, err := htmlindex.Get(label)
	if err != nil {
		return nil, err
	}
	return enc.NewDecoder().Reader(input), nil
}

// dateLayouts are the formats seen in NFO date fields, tried in order.
var dateLayouts = []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339}

func parseDate(vals ...string) *time.Time {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		for _, layout := range dateLayouts {
			if t, err := time.Parse(layout, v); err == nil {
				return &t
			}
		}
	}
	return nil
}

// splitGenres normalises genre values: Kodi sometimes packs several into one
// element separated by "/", so split on that and drop blanks/duplicates.
func splitGenres(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range in {
		for _, g := range strings.Split(raw, "/") {
			g = strings.TrimSpace(g)
			if g == "" || seen[g] {
				continue
			}
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}

func trimmed(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (r *rawNFO) toInfo() *Info {
	info := &Info{
		Title:          strings.TrimSpace(r.Title),
		Tagline:        strings.TrimSpace(r.Tagline),
		Year:           r.Year,
		OfficialRating: strings.TrimSpace(r.MPAA),
		Genres:         splitGenres(r.Genres),
		Studios:        trimmed(r.Studios),
		Season:         r.Season,
		Episode:        r.Episode,
	}
	// Plot is the full synopsis; fall back to the shorter outline.
	if info.Overview = strings.TrimSpace(r.Plot); info.Overview == "" {
		info.Overview = strings.TrimSpace(r.Outline)
	}
	info.PremiereDate = parseDate(r.Premiered, r.Aired, r.ReleaseDate)
	info.CommunityRating = r.communityRating()
	info.People = r.people()
	return info
}

// communityRating prefers the simple <rating>, then the default entry in a
// nested <ratings> block, then the first nested entry.
func (r *rawNFO) communityRating() *float32 {
	if r.Rating != nil && *r.Rating > 0 {
		return r.Rating
	}
	var first *float32
	for i := range r.Ratings.Rating {
		v := r.Ratings.Rating[i].Value
		if r.Ratings.Rating[i].Default && v > 0 {
			return &v
		}
		if first == nil && v > 0 {
			first = &v
		}
	}
	return first
}

func (r *rawNFO) people() []Person {
	var people []Person
	for _, a := range r.Actors {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		people = append(people, Person{Name: name, Role: strings.TrimSpace(a.Role), Type: "Actor"})
	}
	for _, d := range trimmed(r.Directors) {
		people = append(people, Person{Name: d, Type: "Director"})
	}
	// Kodi writes screenwriters as <credits> and/or <writer>.
	for _, w := range trimmed(append(append([]string{}, r.Writers...), r.Credits...)) {
		people = append(people, Person{Name: w, Type: "Writer"})
	}
	return people
}

const nfoExt = ".nfo"

// parseIfExists parses path when it exists, returning nil for a missing file or
// any read/parse error — NFO metadata is best-effort and never aborts a scan.
func parseIfExists(path string) *Info {
	info, err := Parse(path)
	if err != nil {
		return nil
	}
	return info
}

// belowRoot reports whether dir is strictly nested inside libRoot (not libRoot
// itself and not outside it). It is the guard that stops a generic NFO sitting
// directly in a library root from being attached to a bare top-level file.
func belowRoot(dir, libRoot string) bool {
	rel, err := filepath.Rel(libRoot, dir)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Movie loads metadata for a movie file. It reads a sidecar "<name>.nfo" first
// and a generic "movie.nfo" sibling second (sidecar values win). The generic
// "movie.nfo" is only consulted when the file lives in its own sub-directory,
// so a stray NFO directly in the library root is never attached to a bare
// top-level movie file.
func Movie(mediaPath, libRoot string) *Info {
	dir := filepath.Dir(mediaPath)
	sidecar := parseIfExists(sidecarPath(mediaPath))
	if !belowRoot(dir, libRoot) {
		return sidecar
	}
	generic := parseIfExists(filepath.Join(dir, "movie"+nfoExt))
	return merge(generic, sidecar)
}

// Episode loads metadata from a sidecar "<name>.nfo" beside an episode file.
func Episode(mediaPath string) *Info {
	return parseIfExists(sidecarPath(mediaPath))
}

// Track loads metadata from a sidecar "<name>.nfo" beside an audio file.
func Track(mediaPath string) *Info {
	return parseIfExists(sidecarPath(mediaPath))
}

// Series locates and parses a "tvshow.nfo" for the show that contains
// mediaPath, searching from the file's directory upward to — but not
// including — libRoot. Stopping below libRoot is the guard that prevents a
// "tvshow.nfo" placed loosely in the library root from being applied to a flat
// file that has no dedicated show folder.
func Series(mediaPath, libRoot string) *Info {
	return findUpward(filepath.Dir(mediaPath), libRoot, "tvshow"+nfoExt)
}

// Season parses a "season.nfo" in the episode file's directory, provided that
// directory is nested below libRoot.
func Season(mediaPath, libRoot string) *Info {
	dir := filepath.Dir(mediaPath)
	if !belowRoot(dir, libRoot) {
		return nil
	}
	return parseIfExists(filepath.Join(dir, "season"+nfoExt))
}

// Album parses an "album.nfo" in the track file's directory, provided that
// directory is nested below libRoot.
func Album(mediaPath, libRoot string) *Info {
	dir := filepath.Dir(mediaPath)
	if !belowRoot(dir, libRoot) {
		return nil
	}
	return parseIfExists(filepath.Join(dir, "album"+nfoExt))
}

// Artist locates and parses an "artist.nfo" for the album-artist that owns a
// track, searching from the album directory's parent upward to (but not
// including) libRoot.
func Artist(mediaPath, libRoot string) *Info {
	return findUpward(filepath.Dir(filepath.Dir(mediaPath)), libRoot, "artist"+nfoExt)
}

// sidecarPath returns the "<name>.nfo" path for a media file.
func sidecarPath(mediaPath string) string {
	return strings.TrimSuffix(mediaPath, filepath.Ext(mediaPath)) + nfoExt
}

// findUpward walks from startDir up toward libRoot (exclusive) looking for a
// file named name, returning the first match parsed.
func findUpward(startDir, libRoot, name string) *Info {
	for dir := startDir; belowRoot(dir, libRoot); dir = filepath.Dir(dir) {
		if info := parseIfExists(filepath.Join(dir, name)); info != nil {
			return info
		}
	}
	return nil
}

// merge overlays the non-empty fields of over onto base, returning a new Info.
// Either argument may be nil.
func merge(base, over *Info) *Info {
	if base == nil {
		return over
	}
	if over == nil {
		return base
	}
	out := *base
	if over.Title != "" {
		out.Title = over.Title
	}
	if over.Overview != "" {
		out.Overview = over.Overview
	}
	if over.Tagline != "" {
		out.Tagline = over.Tagline
	}
	if over.Year != nil {
		out.Year = over.Year
	}
	if over.PremiereDate != nil {
		out.PremiereDate = over.PremiereDate
	}
	if over.CommunityRating != nil {
		out.CommunityRating = over.CommunityRating
	}
	if over.OfficialRating != "" {
		out.OfficialRating = over.OfficialRating
	}
	if len(over.Genres) > 0 {
		out.Genres = over.Genres
	}
	if len(over.Studios) > 0 {
		out.Studios = over.Studios
	}
	if len(over.People) > 0 {
		out.People = over.People
	}
	if over.Season != nil {
		out.Season = over.Season
	}
	if over.Episode != nil {
		out.Episode = over.Episode
	}
	return &out
}
