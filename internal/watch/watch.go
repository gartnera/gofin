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
	logf     func(string, ...any)

	mu     sync.Mutex
	timers map[string]*time.Timer
}

// Option configures a Watcher.
type Option func(*Watcher)

// WithDebounce overrides the post-write settle delay before indexing a file.
func WithDebounce(d time.Duration) Option {
	return func(w *Watcher) { w.debounce = d }
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
		logf:     log.Printf,
		timers:   map[string]*time.Timer{},
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
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
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
// items beneath it.
func (w *Watcher) handleRemove(ctx context.Context, path string) {
	lib := w.libFor(path)
	if lib == nil {
		return
	}
	if err := w.sc.RemovePath(ctx, path); err != nil {
		w.logf("watch: remove %q: %v", path, err)
	}
	if err := w.sc.RemovePrefix(ctx, path); err != nil {
		w.logf("watch: remove subtree %q: %v", path, err)
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

// addTree registers a watch on dir and every subdirectory.
func (w *Watcher) addTree(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if err := w.fsw.Add(p); err != nil {
			w.logf("watch: add %q: %v", p, err)
		}
		return nil
	})
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
