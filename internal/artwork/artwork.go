// Package artwork locates local poster/cover/thumbnail image files that sit
// alongside (or in a parent directory of) a media file, following the same
// Kodi/Jellyfin layout conventions as the local NFO metadata in internal/nfo.
//
// Discovery is purely filesystem path resolution plus existence checks — no
// decoding — so it is cheap and best-effort: a missing or unreadable image
// yields an empty path and never aborts a scan. Images embedded inside media
// files (e.g. ID3 cover art) are out of scope; only standalone image files on
// disk are surfaced.
//
// See https://jellyfin.org/docs/general/server/media/images for the naming
// conventions mirrored here.
package artwork

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// imageExts are the image file extensions recognised as artwork, in the order
// preferred when several siblings share a base name. ".tbn" is Kodi's legacy
// thumbnail extension (a JPEG by another name).
var imageExts = []string{".jpg", ".jpeg", ".png", ".webp", ".bmp", ".gif", ".tbn"}

// genericFolderImages are the base names of a folder-level primary image, in
// preference order. These name a poster/cover for whatever item owns the
// directory (a movie in its own folder, a series, a season, an album, ...).
var genericFolderImages = []string{"poster", "folder", "cover", "default"}

// fileExists reports whether path names an existing regular (non-directory)
// file, following symlinks like the rest of the scanner.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// firstInDir returns the first existing file in dir whose name is one of the
// given base names with a recognised image extension, trying base names in
// order and, within each, extensions in preference order.
func firstInDir(dir string, names ...string) string {
	for _, n := range names {
		for _, ext := range imageExts {
			p := filepath.Join(dir, n+ext)
			if fileExists(p) {
				return p
			}
		}
	}
	return ""
}

// baseNoExt returns the file name of mediaPath without its directory or
// extension (e.g. "/m/Inception (2010).mkv" -> "Inception (2010)").
func baseNoExt(mediaPath string) string {
	b := filepath.Base(mediaPath)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

// belowRoot reports whether dir is strictly nested inside libRoot (not libRoot
// itself and not outside it). It is the guard that stops a generic image
// sitting directly in a library root from being attached to a bare top-level
// media file. Kept in step with internal/nfo's identically-named guard.
func belowRoot(dir, libRoot string) bool {
	rel, err := filepath.Rel(libRoot, dir)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// findTopmost walks from startDir up toward libRoot (exclusive) and returns the
// match in the directory closest to libRoot. A series/artist poster lives in
// the top-level show/artist folder, so when an intermediate directory (a
// season, an album) also carries a generic image the higher-level one wins —
// otherwise a season's folder.jpg would be mistaken for the series poster.
func findTopmost(startDir, libRoot string, names []string) string {
	best := ""
	for dir := startDir; belowRoot(dir, libRoot); dir = filepath.Dir(dir) {
		if p := firstInDir(dir, names...); p != "" {
			best = p
		}
	}
	return best
}

// Movie resolves the primary image for a movie file. A per-file sidecar
// ("<name>-poster.jpg", "<name>-thumb.jpg" or "<name>.jpg") wins; failing that,
// a generic folder image (poster/folder/cover/default) is used, but only when
// the file lives in its own sub-directory — so a stray image directly in the
// library root is never attached to a bare top-level movie file.
func Movie(mediaPath, libRoot string) string {
	dir := filepath.Dir(mediaPath)
	base := baseNoExt(mediaPath)
	if p := firstInDir(dir, base+"-poster", base+"-thumb", base); p != "" {
		return p
	}
	if !belowRoot(dir, libRoot) {
		return ""
	}
	return firstInDir(dir, append([]string{}, append(genericFolderImages, "movie")...)...)
}

// Episode resolves the thumbnail for an episode file: a sidecar
// "<name>-thumb.jpg" or "<name>.jpg" beside it.
func Episode(mediaPath string) string {
	dir := filepath.Dir(mediaPath)
	base := baseNoExt(mediaPath)
	return firstInDir(dir, base+"-thumb", base)
}

// Series resolves the poster for the show containing mediaPath, searching from
// the file's directory upward to — but not including — libRoot for a generic
// folder image (poster/folder/cover/default).
func Series(mediaPath, libRoot string) string {
	return findTopmost(filepath.Dir(mediaPath), libRoot, genericFolderImages)
}

// Season resolves the poster for a season. It first looks for the Jellyfin
// "seasonNN-poster"/"seasonNN" naming in the series directory (the episode
// file's parent), then for a generic folder image in the episode's own
// directory (the common "Series/Season 01/poster.jpg" layout). seasonNum is the
// parsed season number.
func Season(mediaPath, libRoot string, seasonNum int) string {
	dir := filepath.Dir(mediaPath)
	if !belowRoot(dir, libRoot) {
		return ""
	}
	parent := filepath.Dir(dir)
	if belowRoot(parent, libRoot) {
		named := []string{
			fmt.Sprintf("season%02d-poster", seasonNum),
			fmt.Sprintf("season%02d", seasonNum),
		}
		if p := firstInDir(parent, named...); p != "" {
			return p
		}
	}
	return firstInDir(dir, genericFolderImages...)
}

// Artist resolves the image for the album-artist that owns a track, searching
// from the album directory's parent upward to (but not including) libRoot.
func Artist(mediaPath, libRoot string) string {
	return findTopmost(filepath.Dir(filepath.Dir(mediaPath)), libRoot, append([]string{}, append(genericFolderImages, "artist")...))
}

// Album resolves the cover for the album containing a track: a generic folder
// image in the track file's directory, provided that directory is nested below
// libRoot.
func Album(mediaPath, libRoot string) string {
	dir := filepath.Dir(mediaPath)
	if !belowRoot(dir, libRoot) {
		return ""
	}
	return firstInDir(dir, genericFolderImages...)
}
