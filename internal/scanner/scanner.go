package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/library"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/metadata"
	"github.com/gartnera/gofin/internal/nfo"
	"github.com/gartnera/gofin/internal/probe"
	"github.com/google/uuid"
)

// pending is a media file discovered by walk, awaiting indexing.
type pending struct {
	path string
	info os.FileInfo
}

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

	// Remote metadata enrichment. meta defaults to metadata.Noop so the
	// enrichment path is inert unless a real provider is configured;
	// metaEnabled gates whether the background enricher runs and items are
	// enqueued at all (so default behavior makes no network calls).
	meta        metadata.Provider
	metaEnabled bool
	imageCache  string        // dir for downloaded posters; "" disables image caching
	metaTTL     time.Duration // freshness window for cached remote responses
	enrichQ     chan uuid.UUID
	// enqueued dedups in-flight enqueues so a burst (e.g. every episode of one
	// series enqueuing its Series) sends each id at most once until processed.
	enqMu    sync.Mutex
	enqueued map[uuid.UUID]struct{}

	// onChange, when set, is invoked after a public method mutates the index
	// (a scan, a single-file index, a removal/prune). It is the hook the socket
	// hub uses to push LibraryChanged events; it must be cheap and non-blocking
	// (it is called while mu is held) — see WithChangeHook.
	onChange func()
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
//   - probeCache holds the probe results for the chunk of files currently being
//     indexed, populated concurrently by prefetchProbes (ffprobe is the dominant
//     cost of a real-media scan, ~40ms/file serially). Bounded to one chunk.
type scanCache struct {
	folders    map[folderKey]*ent.MediaItem
	byPath     map[string]*ent.MediaItem
	probeCache map[string]probe.Result
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
	// Look the paths up in batches: a single IN(...) binding one host parameter
	// per path would blow SQLite's SQLITE_MAX_VARIABLE_NUMBER limit for a large
	// flat directory (e.g. thousands of movies in one folder).
	const batch = 500
	for start := 0; start < len(paths); start += batch {
		end := start + batch
		if end > len(paths) {
			end = len(paths)
		}
		existing, err := s.client.MediaItem.Query().
			Where(mediaitem.PathIn(paths[start:end]...)).
			All(ctx)
		if err != nil {
			return err
		}
		for _, it := range existing {
			s.cache.byPath[it.Path] = it
		}
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

// WithMetadataProvider enables background remote-metadata enrichment using p.
// Passing a real provider (anything other than metadata.Noop) turns on the
// enricher; without this option the scanner makes no network calls.
func WithMetadataProvider(p metadata.Provider) Option {
	return func(s *Scanner) {
		s.meta = p
		_, noop := p.(metadata.Noop)
		s.metaEnabled = p != nil && !noop
	}
}

// WithImageCacheDir sets the directory into which remote posters are downloaded
// and cached. When empty, remote images are skipped (text metadata still fills).
func WithImageCacheDir(dir string) Option {
	return func(s *Scanner) { s.imageCache = dir }
}

// WithMetadataTTL sets how long a cached remote response is considered fresh.
func WithMetadataTTL(d time.Duration) Option {
	return func(s *Scanner) { s.metaTTL = d }
}

// WithChangeHook registers a callback invoked whenever a public method mutates
// the index (ScanLibrary, Index, RemovePath/RemovePrefix, PruneEmptyFolders).
// Because the watcher and the HTTP refresh endpoints all mutate the index
// through these methods, a single hook here observes every change. It is called
// while the scanner's mutation lock is held, so it must return quickly and not
// block (the socket hub's NotifyLibraryChanged only schedules a debounced
// broadcast).
func WithChangeHook(f func()) Option {
	return func(s *Scanner) { s.onChange = f }
}

// notifyChange fires the change hook if one is registered.
func (s *Scanner) notifyChange() {
	if s.onChange != nil {
		s.onChange()
	}
}

// New returns a Scanner backed by the given ent client.
func New(client *ent.Client, opts ...Option) *Scanner {
	s := &Scanner{
		client:   client,
		meta:     metadata.Noop{},
		metaTTL:  defaultMetaTTL,
		enrichQ:  make(chan uuid.UUID, 1024),
		enqueued: map[uuid.UUID]struct{}{},
	}
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

// probeFile returns probe metadata for a file, preferring the result
// prefetchProbes computed concurrently for the current chunk; absent that
// (e.g. the watcher's single-file path) it probes inline. Empty metadata is
// returned on failure so a bad or unreadable file never aborts a scan.
func (s *Scanner) probeFile(ctx context.Context, path string) probe.Result {
	if s.cache != nil {
		if r, ok := s.cache.probeCache[path]; ok {
			return r
		}
	}
	return s.rawProbe(ctx, path)
}

func (s *Scanner) rawProbe(ctx context.Context, path string) probe.Result {
	res, err := s.prober.Probe(ctx, path)
	if err != nil {
		return probe.Result{}
	}
	return res
}

// probeWorkers is the concurrency used to prefetch probes. ffprobe is an
// out-of-process, largely I/O-bound exec per file, so probing across cores
// turns a serial ~40ms/file scan into a roughly NumCPU-parallel one. Overridable
// via GOFIN_SCAN_PROBE_WORKERS (also lets benchmarks force serial with 1).
func probeWorkers() int {
	if v := os.Getenv("GOFIN_SCAN_PROBE_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	if n := runtime.NumCPU(); n > 0 {
		return n
	}
	return 1
}

// prefetchProbes probes the (changed/new) files of a chunk concurrently and
// returns their results keyed by path, for probeFile to serve during the serial
// index pass. Files unchanged on disk are skipped — the index funcs return
// before probing them anyway. Results are accumulated in a local map and only
// published to the cache after all workers finish, so probeFile (which reads the
// cache) never races a writer.
func (s *Scanner) prefetchProbes(ctx context.Context, chunk []pending) map[string]probe.Result {
	out := make(map[string]probe.Result, len(chunk))
	var todo []string
	for _, f := range chunk {
		if !unchanged(s.cache.byPath[f.path], f.info) {
			todo = append(todo, f.path)
		}
	}
	if len(todo) == 0 {
		return out
	}
	workers := probeWorkers()
	if workers > len(todo) {
		workers = len(todo)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	ch := make(chan string)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range ch {
				r := s.rawProbe(ctx, p)
				mu.Lock()
				out[p] = r
				mu.Unlock()
			}
		}()
	}
	for _, p := range todo {
		ch <- p
	}
	close(ch)
	wg.Wait()
	return out
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
	if err := s.prune(ctx, lib); err != nil {
		return err
	}
	s.notifyChange()
	return nil
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
	if err := fn(ctx, lib, path, info); err != nil {
		return err
	}
	s.notifyChange()
	return nil
}

// walk recursively traverses dir, honouring .ignore files, and invokes fn for
// each file whose extension is in exts. matchers carries the .ignore rules
// inherited from ancestor directories. Symlinked files are followed (probing
// and streaming resolve to the target), which is how `gofin sample` stands up
// large synthetic libraries from a few real files.
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
	// Process the directory in chunks: probe each chunk's files concurrently
	// (prefetchProbes), then index the chunk serially (DB writes and the shared
	// folder cache aren't concurrency-safe). Chunking bounds the probe cache to
	// one chunk regardless of how many files a single directory holds.
	const probeChunk = 256
	for start := 0; start < len(files); start += probeChunk {
		end := start + probeChunk
		if end > len(files) {
			end = len(files)
		}
		chunk := files[start:end]
		if s.cache != nil {
			s.cache.probeCache = s.prefetchProbes(ctx, chunk)
		}
		for _, f := range chunk {
			if err := fn(ctx, lib, f.path, f.info); err != nil {
				return fmt.Errorf("index %q: %w", f.path, err)
			}
		}
	}
	if s.cache != nil {
		s.cache.probeCache = nil
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
	upd := item.Update().SetMtime(0)
	// Clearing the sync marker makes the re-index re-enqueue the item for remote
	// enrichment, so a manual refresh re-pulls metadata (and any newly-available
	// match) in addition to re-probing the file.
	if s.metaEnabled {
		upd = upd.ClearMetadataSyncedAt()
	}
	if err := upd.Exec(ctx); err != nil {
		return err
	}
	return s.Index(ctx, lib, item.Path)
}

// RemovePath deletes the item backed by the given file path, if any, and
// reports how many rows were removed (0 or 1) so a caller can tell a real
// single-file removal from a no-op (e.g. a directory event whose path matches no
// playable row).
func (s *Scanner) RemovePath(ctx context.Context, path string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.client.MediaItem.Delete().Where(mediaitem.PathEQ(path)).Exec(ctx)
	if err == nil && n > 0 {
		s.notifyChange()
	}
	return n, err
}

// RemovePrefix deletes every playable item whose file lives under dir (used when
// a directory is removed from disk) and reports how many rows were removed, so a
// caller can tell a directory subtree deletion (n > 0) from a single-file event.
func (s *Scanner) RemovePrefix(ctx context.Context, dir string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := strings.TrimSuffix(dir, string(filepath.Separator)) + string(filepath.Separator)
	n, err := s.client.MediaItem.Delete().Where(mediaitem.PathHasPrefix(prefix)).Exec(ctx)
	if err == nil && n > 0 {
		s.notifyChange()
	}
	return n, err
}

// PruneEmptyFolders drops folder items (Series/Season/Artist/Album) left without
// children, cascading parent-ward. It is the cheap tail of a full prune (no
// per-file stat pass), used by the watcher after a directory removal so an
// emptied show/artist folder disappears immediately instead of lingering until
// the next full rescan.
func (s *Scanner) PruneEmptyFolders(ctx context.Context, lib *ent.Library) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneEmptyFolders(ctx, lib)
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
	// Drop any folder rows left childless once their files are gone.
	return s.pruneEmptyFolders(ctx, lib)
}

// pruneEmptyFolders repeatedly drops folder items with no children so that,
// e.g., an emptied season is removed before its now-childless series. Caller
// must hold s.mu.
func (s *Scanner) pruneEmptyFolders(ctx context.Context, lib *ent.Library) error {
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
