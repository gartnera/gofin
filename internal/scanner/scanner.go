package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/library"
	"github.com/gartnera/gofin/ent/mediaitem"
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
}

// New returns a Scanner backed by the given ent client.
func New(client *ent.Client) *Scanner {
	return &Scanner{client: client}
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

// ScanLibrary walks a library's directory and indexes its media, dispatching
// on the library's declared type.
func (s *Scanner) ScanLibrary(ctx context.Context, lib *ent.Library) error {
	switch lib.Type {
	case library.TypeMovies:
		return s.walk(ctx, lib, videoExts, s.indexMovie)
	case library.TypeTvshows:
		return s.walk(ctx, lib, videoExts, s.indexEpisode)
	case library.TypeMusic:
		return s.walk(ctx, lib, audioExts, s.indexAudio)
	default:
		return fmt.Errorf("unknown library type %q", lib.Type)
	}
}

type indexFunc func(ctx context.Context, lib *ent.Library, path string) error

// walk traverses the library root and invokes fn for each file whose extension
// is in exts.
func (s *Scanner) walk(ctx context.Context, lib *ent.Library, exts map[string]bool, fn indexFunc) error {
	return filepath.WalkDir(lib.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !exts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if err := fn(ctx, lib, path); err != nil {
			return fmt.Errorf("index %q: %w", path, err)
		}
		return nil
	})
}

// containerOf returns the lowercase extension without the leading dot.
func containerOf(path string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}

// findOrCreateFolder looks up a folder-like item by (kind, name) within a
// library and optional parent, creating it if absent.
func (s *Scanner) findOrCreateFolder(ctx context.Context, lib *ent.Library, kind mediaitem.Kind, name string, parentID *uuid.UUID) (*ent.MediaItem, error) {
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

	create := s.client.MediaItem.Create().
		SetKind(kind).
		SetName(name).
		SetSortName(strings.ToLower(name)).
		SetLibrary(lib)
	if parentID != nil {
		create = create.SetParentID(*parentID)
	}
	return create.Save(ctx)
}

// existingByPath returns the indexed item for a file path, or (nil, nil) if the
// file has not been indexed yet.
func (s *Scanner) existingByPath(ctx context.Context, path string) (*ent.MediaItem, error) {
	it, err := s.client.MediaItem.Query().Where(mediaitem.PathEQ(path)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return it, err
}
