package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/library"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/nfo"
	"github.com/gartnera/gofin/internal/probe"
	"github.com/google/uuid"
)

var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true,
	".m4v": true, ".webm": true, ".ts": true, ".wmv": true,
}

var audioExts = map[string]bool{
	".mp3": true, ".flac": true, ".m4a": true, ".ogg": true,
	".opus": true, ".wav": true, ".aac": true, ".wma": true,
}

// Scanner indexes media libraries into the ent-backed database.
type Scanner struct {
	client *ent.Client
	prober probe.Prober
	// mu serialises index mutations so that a full library scan and the
	// filesystem watcher cannot race when creating shared folder rows.
	mu sync.Mutex
	// cache, when non-nil, accelerates the ScanLibrary currently in progress.
	// It is only set for the duration of a ScanLibrary call (under mu); the
	// watcher's single-file Index path leaves it nil and queries directly.
	cache *scanCache
}

// folderKey identifies a folder-like item within a library by its kind, name,
// and parent (uuid.Nil for top-level folders) — the same tuple
// findOrCreateFolder dedupes on.
type folderKey struct {
	kind   mediaitem.Kind
	name   string
	parent uuid.UUID
}

// scanCache accelerates a single ScanLibrary pass without holding the whole
// library in memory.
//
//   - folders is the complete set of folder-like rows (Series/Season/Artist/
//     Album), loaded once. These are inherently bounded — a library has far
//     fewer folders than files, and they carry no probe JSON — and must persist
//     across directories because one series' seasons can live in unrelated
//     directories, so caching them avoids a lookup-or-create query per file.
//   - byPath holds existing playable rows for the directory currently being
//     walked, refreshed per directory by walk via one batched query. This keeps
//     resident memory bounded by the largest single directory rather than by
//     the library size, so a 500k-item library is no heavier than a 500-item
//     one.
type scanCache struct {
	folders map[folderKey]*ent.MediaItem
	byPath  map[string]*ent.MediaItem
}

func folderKeyOf(f *ent.MediaItem) folderKey {
	k := folderKey{kind: f.Kind, name: f.Name}
	if f.Edges.Parent != nil {
		k.parent = f.Edges.Parent.ID
	}
	return k
}

// loadFolders snapshots a library's folder rows (the bounded set) keyed by
// (kind, name, parent). Playable rows are loaded lazily, per directory, in walk.
func (s *Scanner) loadFolders(ctx context.Context, lib *ent.Library) (*scanCache, error) {
	c := &scanCache{
		folders: map[folderKey]*ent.MediaItem{},
		byPath:  map[string]*ent.MediaItem{},
	}
	folders, err := s.client.MediaItem.Query().
		Where(mediaitem.HasLibraryWith(library.ID(lib.ID)), mediaitem.PathEQ("")).
		WithParent().
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range folders {
		c.folders[folderKeyOf(f)] = f
	}
	return c, nil
}

// loadDirPaths replaces the cache's byPath map with the existing playable rows
// for exactly the given file paths, fetched in one query. Called by walk before
// indexing a directory's files so existingByPath resolves from memory without a
// per-file round trip, while keeping only one directory resident at a time.
func (s *Scanner) loadDirPaths(ctx context.Context, paths []string) error {
	s.cache.byPath = make(map[string]*ent.MediaItem, len(paths))
	if len(paths) == 0 {
		return nil
	}
	existing, err := s.client.MediaItem.Query().
		Where(mediaitem.PathIn(paths...)).
		All(ctx)
	if err != nil {
		return err
	}
	for _, it := range existing {
		s.cache.byPath[it.Path] = it
	}
	return nil
}

// Option configures a Scanner.
type Option func(*Scanner)

// WithProber sets the media prober used to extract durations and stream
// metadata. By default the scanner auto-detects ffprobe and falls back to a
// no-op prober when it is unavailable.
func WithProber(p probe.Prober) Option {
	return func(s *Scanner) { s.prober = p }
}

// New returns a Scanner backed by the given ent client.
func New(client *ent.Client, opts ...Option) *Scanner {
	s := &Scanner{client: client}
	for _, opt := range opts {
		opt(s)
	}
	if s.prober == nil {
		if ff, ok := probe.Available(); ok {
			s.prober = ff
		} else {
			s.prober = probe.Noop{}
		}
	}
	return s
}

// probeFile probes a media file, returning empty metadata on failure so a bad
// or unreadable file never aborts a scan.
func (s *Scanner) probeFile(ctx context.Context, path string) probe.Result {
	res, err := s.prober.Probe(ctx, path)
	if err != nil {
		return probe.Result{}
	}
	return res
}

// applyNFO overlays metadata parsed from a local NFO file onto an item that has
// already been created/updated with its core (filename- and probe-derived)
// fields. It is a no-op when nf is nil, so callers can pass the result of an
// nfo lookup directly. Scalar NFO sources are authoritative for the fields they
// own, so absent values clear any stale metadata from a previous index — except
// where the user has locked a field (or the whole item), which always wins over
// the NFO, mirroring how the filename/probe pass treats metaLocked.
func (s *Scanner) applyNFO(ctx context.Context, item *ent.MediaItem, nf *nfo.Info) error {
	if nf == nil || item.LockData {
		return nil
	}
	// Field names are Jellyfin MetadataField values; those without an
	// equivalent (Tagline/CommunityRating/PremiereDate) are governed only by the
	// whole-item LockData guard above.
	upd := item.Update()
	if !metaLocked(item, "Overview") {
		upd.SetOverview(nf.Overview)
	}
	upd.SetTagline(nf.Tagline)
	if !metaLocked(item, "OfficialRating") {
		upd.SetOfficialRating(nf.OfficialRating)
	}
	if !metaLocked(item, "Genres") {
		upd.SetGenres(nf.Genres)
	}
	if !metaLocked(item, "Studios") {
		upd.SetStudios(nf.Studios)
	}
	if !metaLocked(item, "Cast") {
		upd.SetPeople(nf.People)
	}
	if nf.Year != nil && !metaLocked(item, "ProductionYear") {
		upd.SetProductionYear(*nf.Year)
	}
	if nf.CommunityRating != nil {
		upd.SetCommunityRating(*nf.CommunityRating)
	} else {
		upd.ClearCommunityRating()
	}
	if nf.PremiereDate != nil {
		upd.SetPremiereDate(*nf.PremiereDate)
	} else {
		upd.ClearPremiereDate()
	}
	if err := upd.Exec(ctx); err != nil {
		return err
	}
	// Keep the in-memory item consistent with what was just persisted. A folder
	// row reused from the scan cache is enriched once on creation; without this
	// its child files would each see the stale (bare) struct and re-read the
	// NFO from disk on every pass.
	if !metaLocked(item, "Overview") {
		item.Overview = nf.Overview
	}
	if !metaLocked(item, "Genres") {
		item.Genres = nf.Genres
	}
	if !metaLocked(item, "Studios") {
		item.Studios = nf.Studios
	}
	if !metaLocked(item, "Cast") {
		item.People = nf.People
	}
	if nf.Year != nil && !metaLocked(item, "ProductionYear") {
		item.ProductionYear = nf.Year
	}
	return nil
}

// EnsureLibrary creates or updates a Library row keyed by its path so that
// repeated scans reuse the same record.
func (s *Scanner) EnsureLibrary(ctx context.Context, name, typ, path string) (*ent.Library, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path %q: %w", path, err)
	}
	existing, err := s.client.Library.Query().Where(library.PathEQ(abs)).Only(ctx)
	switch {
	case err == nil:
		return existing.Update().SetName(name).SetType(library.Type(typ)).Save(ctx)
	case ent.IsNotFound(err):
		return s.client.Library.Create().
			SetName(name).
			SetType(library.Type(typ)).
			SetPath(abs).
			Save(ctx)
	default:
		return nil, err
	}
}

type indexFunc func(ctx context.Context, lib *ent.Library, path string, info os.FileInfo) error

// dispatch returns the media extensions and index function for a library type.
func (s *Scanner) dispatch(typ library.Type) (map[string]bool, indexFunc, error) {
	switch typ {
	case library.TypeMovies:
		return videoExts, s.indexMovie, nil
	case library.TypeTvshows:
		return videoExts, s.indexEpisode, nil
	case library.TypeMusic:
		return audioExts, s.indexAudio, nil
	default:
		return nil, nil, fmt.Errorf("unknown library type %q", typ)
	}
}

// ScanLibrary walks a library's directory and indexes its media, dispatching on
// the library's declared type. Files already indexed and unchanged on disk are
// skipped; items whose backing files have disappeared are pruned.
func (s *Scanner) ScanLibrary(ctx context.Context, lib *ent.Library) error {
	exts, fn, err := s.dispatch(lib.Type)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cache, err := s.loadFolders(ctx, lib)
	if err != nil {
		return err
	}
	s.cache = cache
	defer func() { s.cache = nil }()
	if err := s.walk(ctx, lib, lib.Path, exts, fn, nil); err != nil {
		return err
	}
	return s.prune(ctx, lib)
}

// Index indexes (or re-indexes) a single file discovered by the watcher. It is
// safe to call concurrently with ScanLibrary. Files outside the library type's
// extensions or excluded by a .ignore file are silently skipped.
func (s *Scanner) Index(ctx context.Context, lib *ent.Library, path string) error {
	exts, fn, err := s.dispatch(lib.Type)
	if err != nil {
		return err
	}
	if !exts[strings.ToLower(filepath.Ext(path))] {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isIgnored(lib, path) {
		return nil
	}
	return fn(ctx, lib, path, info)
}

// walk recursively traverses dir, honouring .ignore files, and invokes fn for
// each file whose extension is in exts. matchers carries the .ignore rules
// inherited from ancestor directories.
func (s *Scanner) walk(ctx context.Context, lib *ent.Library, dir string, exts map[string]bool, fn indexFunc, matchers []ignoreMatcher) error {
	m, skipAll, err := loadIgnore(dir)
	if err != nil {
		return err
	}
	if skipAll {
		return nil
	}
	if m != nil {
		// Copy so sibling recursions don't observe each other's appends.
		next := make([]ignoreMatcher, len(matchers), len(matchers)+1)
		copy(next, matchers)
		matchers = append(next, *m)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	// Index this directory's media files first, then recurse into subdirectories.
	// Separating the two lets us resolve all of this directory's existing rows in
	// a single batched query (loadDirPaths) instead of one lookup per file, while
	// keeping only one directory's worth of rows resident at a time.
	type pending struct {
		path string
		info os.FileInfo
	}
	var files []pending
	var subdirs []string
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if ignored(matchers, full, true) {
				continue
			}
			subdirs = append(subdirs, full)
			continue
		}
		if !exts[strings.ToLower(filepath.Ext(full))] {
			continue
		}
		if ignored(matchers, full, false) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		files = append(files, pending{full, info})
	}

	if s.cache != nil {
		paths := make([]string, len(files))
		for i, f := range files {
			paths[i] = f.path
		}
		if err := s.loadDirPaths(ctx, paths); err != nil {
			return err
		}
	}
	for _, f := range files {
		if err := fn(ctx, lib, f.path, f.info); err != nil {
			return fmt.Errorf("index %q: %w", f.path, err)
		}
	}

	for _, sub := range subdirs {
		if err := s.walk(ctx, lib, sub, exts, fn, matchers); err != nil {
			return err
		}
	}
	return nil
}

// isIgnored reports whether path is excluded by a .ignore file anywhere between
// the library root and the file's own directory.
func (s *Scanner) isIgnored(lib *ent.Library, path string) bool {
	rel, err := filepath.Rel(lib.Path, path)
	if err != nil {
		return false
	}
	var matchers []ignoreMatcher
	dir := lib.Path
	// Walk from the library root down to (but not including) the file itself.
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := 0; i < len(parts); i++ {
		m, skipAll, err := loadIgnore(dir)
		if err == nil {
			if skipAll {
				return true
			}
			if m != nil {
				matchers = append(matchers, *m)
			}
		}
		if i == len(parts)-1 {
			break
		}
		dir = filepath.Join(dir, parts[i])
		if ignored(matchers, dir, true) {
			return true
		}
	}
	return ignored(matchers, path, false)
}

// containerOf returns the lowercase extension without the leading dot.
func containerOf(path string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}

// findOrCreateFolder looks up a folder-like item by (kind, name) within a
// library and optional parent, creating it if absent.
func (s *Scanner) findOrCreateFolder(ctx context.Context, lib *ent.Library, kind mediaitem.Kind, name string, parentID *uuid.UUID) (*ent.MediaItem, error) {
	key := folderKey{kind: kind, name: name}
	if parentID != nil {
		key.parent = *parentID
	}

	if s.cache != nil {
		// The cache is a complete snapshot of the library plus every folder
		// created so far this scan, so a hit is authoritative and a miss means
		// the folder genuinely doesn't exist — skip the redundant lookup.
		if f, ok := s.cache.folders[key]; ok {
			return f, nil
		}
	} else {
		q := s.client.MediaItem.Query().
			Where(
				mediaitem.KindEQ(kind),
				mediaitem.NameEQ(name),
				mediaitem.HasLibraryWith(library.ID(lib.ID)),
			)
		if parentID == nil {
			q = q.Where(mediaitem.Not(mediaitem.HasParent()))
		} else {
			q = q.Where(mediaitem.HasParentWith(mediaitem.ID(*parentID)))
		}
		existing, err := q.Only(ctx)
		if err == nil {
			return existing, nil
		}
		if !ent.IsNotFound(err) {
			return nil, err
		}
	}

	create := s.client.MediaItem.Create().
		SetKind(kind).
		SetName(name).
		SetSortName(strings.ToLower(name)).
		SetLibrary(lib)
	if parentID != nil {
		create = create.SetParentID(*parentID)
	}
	item, err := create.Save(ctx)
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		s.cache.folders[key] = item
	}
	return item, nil
}

// existingByPath returns the indexed item for a file path, or (nil, nil) if the
// file has not been indexed yet.
func (s *Scanner) existingByPath(ctx context.Context, path string) (*ent.MediaItem, error) {
	if s.cache != nil {
		// nil map value (absent) correctly reports "not indexed yet".
		return s.cache.byPath[path], nil
	}
	it, err := s.client.MediaItem.Query().Where(mediaitem.PathEQ(path)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return it, err
}

// unchanged reports whether an already-indexed item matches the file's current
// size and modification time, in which case it can be skipped without probing.
func unchanged(it *ent.MediaItem, info os.FileInfo) bool {
	return it != nil && it.Size == info.Size() && it.Mtime == info.ModTime().UnixNano()
}

// metaLocked reports whether a user-edited metadata field on an existing item
// must be preserved rather than re-derived from the file. A whole-item lock
// (LockData, Jellyfin's "Lock this item" checkbox) covers every field; a
// per-field lock covers only that Jellyfin MetadataField name (e.g. "Name").
// Fields with no MetadataField equivalent (e.g. "ProductionYear") are governed
// solely by the whole-item lock.
func metaLocked(it *ent.MediaItem, field string) bool {
	if it == nil {
		return false
	}
	if it.LockData {
		return true
	}
	for _, f := range it.LockedFields {
		if f == field {
			return true
		}
	}
	return false
}

// RefreshItem forces a re-probe and re-index of a single item by clearing its
// stored mtime so the change check fails, then re-indexing the file.
func (s *Scanner) RefreshItem(ctx context.Context, item *ent.MediaItem) error {
	if item.Path == "" {
		return nil
	}
	lib, err := item.QueryLibrary().Only(ctx)
	if err != nil {
		return err
	}
	if err := item.Update().SetMtime(0).Exec(ctx); err != nil {
		return err
	}
	return s.Index(ctx, lib, item.Path)
}

// RemovePath deletes the item backed by the given file path, if any.
func (s *Scanner) RemovePath(ctx context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.client.MediaItem.Delete().Where(mediaitem.PathEQ(path)).Exec(ctx)
	return err
}

// RemovePrefix deletes every playable item whose file lives under dir (used when
// a directory is removed from disk).
func (s *Scanner) RemovePrefix(ctx context.Context, dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := strings.TrimSuffix(dir, string(filepath.Separator)) + string(filepath.Separator)
	_, err := s.client.MediaItem.Delete().Where(mediaitem.PathHasPrefix(prefix)).Exec(ctx)
	return err
}

// prune removes items whose backing files have disappeared, then drops any
// folder items left without children. Caller must hold s.mu.
func (s *Scanner) prune(ctx context.Context, lib *ent.Library) error {
	playable, err := s.client.MediaItem.Query().
		Where(mediaitem.HasLibraryWith(library.ID(lib.ID)), mediaitem.PathNEQ("")).
		All(ctx)
	if err != nil {
		return err
	}
	for _, it := range playable {
		if _, statErr := os.Stat(it.Path); os.IsNotExist(statErr) {
			if err := s.client.MediaItem.DeleteOne(it).Exec(ctx); err != nil {
				return err
			}
		}
	}
	// Repeatedly drop empty folders so that, e.g., an emptied season is removed
	// before its now-childless series.
	for {
		folders, err := s.client.MediaItem.Query().
			Where(mediaitem.HasLibraryWith(library.ID(lib.ID)), mediaitem.PathEQ("")).
			All(ctx)
		if err != nil {
			return err
		}
		removed := 0
		for _, f := range folders {
			n, err := f.QueryChildren().Count(ctx)
			if err != nil {
				return err
			}
			if n == 0 {
				if err := s.client.MediaItem.DeleteOne(f).Exec(ctx); err != nil {
					return err
				}
				removed++
			}
		}
		if removed == 0 {
			return nil
		}
	}
}
