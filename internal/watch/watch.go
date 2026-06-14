// Package watch keeps the media index in sync with the filesystem by reacting
// to inotify-style events: new and modified files are indexed, removed files
// are dropped from the database.
package watch

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/internal/scanner"
)

// defaultDebounce is how long to wait after the last write to a file before
// indexing it, so that large copies are probed only once they settle.
const defaultDebounce = 2 * time.Second

// Watcher indexes filesystem changes under a set of libraries in real time.
type Watcher struct {
	sc       *scanner.Scanner
	fsw      *fsnotify.Watcher
	libs     []*ent.Library
	debounce time.Duration
	window   time.Duration // leaf-directory watch recency window; 0 watches all
	rescan   time.Duration // periodic full-rescan interval; 0 disables it
	now      func() time.Time
	logf     func(string, ...any)

	mu     sync.Mutex
	timers map[string]*time.Timer

	// watched dedupes fsw.Add calls. Only touched from New (before Run) and from
	// the single Run goroutine's event handling, so it needs no lock.
	watched map[string]bool
}

// Option configures a Watcher.
type Option func(*Watcher)

// WithDebounce overrides the post-write settle delay before indexing a file.
func WithDebounce(d time.Duration) Option {
	return func(w *Watcher) { w.debounce = d }
}

// WithWatchWindow bounds which leaf directories get an inotify watch: only
// those modified within d are watched (container directories are always
// watched, so new folders are still detected). A zero d watches every
// directory. This caps the watch count on large, mostly-static libraries that
// would otherwise exhaust Linux's fs.inotify.max_user_watches.
func WithWatchWindow(d time.Duration) Option {
	return func(w *Watcher) { w.window = d }
}

// WithRescanInterval enables a periodic full rescan of every library, healing
// drift in directories left unwatched by the window (e.g. a file replaced in an
// old movie folder). A zero d disables it.
func WithRescanInterval(d time.Duration) Option {
	return func(w *Watcher) { w.rescan = d }
}

// WithLogger sets the logging function (defaults to log.Printf).
func WithLogger(logf func(string, ...any)) Option {
	return func(w *Watcher) { w.logf = logf }
}

// New creates a Watcher and registers recursive watches for each library's
// directory tree. Libraries are matched longest-path-first so nested roots
// resolve correctly.
func New(sc *scanner.Scanner, libs []*ent.Library, opts ...Option) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		sc:       sc,
		fsw:      fsw,
		debounce: defaultDebounce,
		now:      time.Now,
		logf:     log.Printf,
		timers:   map[string]*time.Timer{},
		watched:  map[string]bool{},
	}
	for _, opt := range opts {
		opt(w)
	}
	// Copy and sort by descending path length for unambiguous matching.
	w.libs = append(w.libs, libs...)
	sort.Slice(w.libs, func(i, j int) bool {
		return len(w.libs[i].Path) > len(w.libs[j].Path)
	})
	for _, lib := range w.libs {
		w.addTree(lib.Path)
	}
	return w, nil
}

// Run processes filesystem events until ctx is cancelled, at which point the
// underlying watcher is closed.
func (w *Watcher) Run(ctx context.Context) error {
	defer w.fsw.Close()
	// A nil channel blocks forever, so the rescan case is simply never selected
	// when the periodic rescan is disabled.
	var rescanC <-chan time.Time
	if w.rescan > 0 {
		t := time.NewTicker(w.rescan)
		defer t.Stop()
		rescanC = t.C
	}
	// rescanDone signals (from the background rescan goroutine) that a periodic
	// scan finished, so the Run goroutine — the sole writer of the watched map —
	// can refresh watches without racing it. Buffered so the goroutine never
	// blocks even if Run has already returned. rescanning guards against starting
	// a second scan while one is still in flight (a scan can outlast the interval
	// on a large library).
	rescanDone := make(chan struct{}, 1)
	rescanning := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-rescanC:
			// Run the scan off this goroutine so fsnotify events keep draining
			// (a multi-second scan that blocked the loop would overflow the
			// kernel inotify queue and drop events).
			if !rescanning {
				rescanning = true
				go func() {
					w.rescanScan(ctx)
					select {
					case rescanDone <- struct{}{}:
					case <-ctx.Done():
					}
				}()
			}
		case <-rescanDone:
			rescanning = false
			// Refresh watches on the Run goroutine: directories the scan made
			// recent (or new containers) become watched. addTree only touches
			// watched, so it must stay on this goroutine.
			for _, lib := range w.libs {
				w.addTree(lib.Path)
			}
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.logf("watch: error: %v", err)
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			w.handle(ctx, ev)
		}
	}
}

// rescanScan runs a full scan of every library, re-indexing anything the
// windowed watcher missed and pruning vanished items. It performs no watch
// bookkeeping — the watched map is owned by the Run goroutine, which refreshes
// watches via addTree once this signals completion. Safe to run off the Run
// goroutine: the scanner serialises its own index mutations under s.mu.
func (w *Watcher) rescanScan(ctx context.Context) {
	for _, lib := range w.libs {
		if ctx.Err() != nil {
			return
		}
		if err := w.sc.ScanLibrary(ctx, lib); err != nil {
			w.logf("watch: periodic rescan %q: %v", lib.Path, err)
		}
	}
}

// handle dispatches a single filesystem event.
func (w *Watcher) handle(ctx context.Context, ev fsnotify.Event) {
	switch {
	case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
		w.handleRemove(ctx, ev.Name)
	case ev.Op&(fsnotify.Create|fsnotify.Write) != 0:
		w.handleUpsert(ctx, ev.Name, ev.Op&fsnotify.Create != 0)
	}
}

// handleUpsert reacts to a created or written path. Directories get watched and
// scanned for pre-existing contents (e.g. a folder moved in); files are indexed
// after a debounce.
func (w *Watcher) handleUpsert(ctx context.Context, path string, created bool) {
	info, err := os.Stat(path)
	if err != nil {
		return // vanished between event and stat; ignore
	}
	if info.IsDir() {
		if created {
			w.addTree(path)
			w.indexTree(ctx, path)
		}
		return
	}
	w.schedule(ctx, path)
}

// handleRemove drops the item at path and, in case path was a directory, any
// items beneath it. A deleted folder fires this for the entry on its still-
// watched parent (the library root is always watched), so removing a whole
// movie/show/artist folder is caught even when the folder itself was a stale,
// unwatched leaf.
func (w *Watcher) handleRemove(ctx context.Context, path string) {
	lib := w.libFor(path)
	if lib == nil {
		return
	}
	// Drop the inotify watch and bookkeeping for the gone subtree so the path
	// can be re-watched if it reappears.
	w.unwatch(path)
	removed, err := w.sc.RemovePath(ctx, path)
	if err != nil {
		w.logf("watch: remove %q: %v", path, err)
	}
	n, err := w.sc.RemovePrefix(ctx, path)
	if err != nil {
		w.logf("watch: remove subtree %q: %v", path, err)
	}
	// If anything was removed — a single file (removed > 0) or a whole directory
	// subtree (n > 0) — its now-childless Series/Season/Artist/Album folder rows
	// must be pruned too (RemovePath/RemovePrefix only delete the playable
	// files). Scoped to this library and folder-only, so it is far cheaper than a
	// full rescan. Deleting the last file in a season otherwise leaves the empty
	// Season/Series rows lingering until the next periodic rescan.
	if removed > 0 || n > 0 {
		if err := w.sc.PruneEmptyFolders(ctx, lib); err != nil {
			w.logf("watch: prune empty folders under %q: %v", path, err)
		}
	}
}

// unwatch removes path and everything beneath it from the watch set so a
// recreated directory is watched afresh rather than skipped as already-added.
// It also drops the kernel watch: on a genuine deletion inotify has already
// removed it (Remove returns a harmless not-found error), but on a rename the
// inode persists, so this is what actually reclaims the watch.
func (w *Watcher) unwatch(path string) {
	prefix := path + string(filepath.Separator)
	for p := range w.watched {
		if p == path || strings.HasPrefix(p, prefix) {
			_ = w.fsw.Remove(p)
			delete(w.watched, p)
		}
	}
}

// schedule (re)arms a per-path debounce timer that indexes the file once writes
// stop arriving.
func (w *Watcher) schedule(ctx context.Context, path string) {
	lib := w.libFor(path)
	if lib == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.timers[path]; ok {
		t.Stop()
	}
	w.timers[path] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		delete(w.timers, path)
		w.mu.Unlock()
		if err := w.sc.Index(ctx, lib, path); err != nil {
			w.logf("watch: index %q: %v", path, err)
		}
	})
}

// indexTree indexes every media file already present under dir (used when a
// populated directory appears).
func (w *Watcher) indexTree(ctx context.Context, dir string) {
	lib := w.libFor(dir)
	if lib == nil {
		return
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if err := w.sc.Index(ctx, lib, p); err != nil {
			w.logf("watch: index %q: %v", p, err)
		}
		return nil
	})
}

// addTree registers inotify watches under dir. The tree root and every
// container directory (one with at least one subdirectory) are always watched,
// so the creation of a new child directory anywhere structural is detected and
// gets its own subtree watched in turn. Leaf directories are watched only when
// modified within the recency window — they are the bulk of a media tree and
// rarely change, so skipping the stale ones keeps the watch count bounded by
// recent activity rather than library size. A zero window watches everything.
func (w *Watcher) addTree(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != dir {
			// Descending into p proves filepath.Dir(p) is a container: watch it
			// regardless of age so new child directories under it are seen.
			w.add(filepath.Dir(p))
		}
		// Watch p itself if it is the tree root or recent enough; a stale leaf is
		// left unwatched (the periodic rescan is the backstop for it).
		if p == dir || w.recent(p) {
			w.add(p)
		}
		return nil
	})
}

// recent reports whether dir was modified within the watch window. A zero
// window (the default) treats every directory as recent.
func (w *Watcher) recent(dir string) bool {
	if w.window <= 0 {
		return true
	}
	info, err := os.Stat(dir)
	if err != nil {
		return false
	}
	return w.now().Sub(info.ModTime()) <= w.window
}

// add registers a single inotify watch, deduping so a container visited once
// per child is only added once.
func (w *Watcher) add(dir string) {
	if w.watched[dir] {
		return
	}
	if err := w.fsw.Add(dir); err != nil {
		w.logf("watch: add %q: %v", dir, err)
		return
	}
	w.watched[dir] = true
}

// libFor returns the library whose path contains the given path.
func (w *Watcher) libFor(path string) *ent.Library {
	for _, lib := range w.libs {
		if path == lib.Path || strings.HasPrefix(path, lib.Path+string(filepath.Separator)) {
			return lib
		}
	}
	return nil
}
