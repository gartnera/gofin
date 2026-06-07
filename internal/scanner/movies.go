package scanner

import (
	"context"
	"os"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/nfo"
)

// indexMovie indexes a single video file in a movies library as a Movie.
func (s *Scanner) indexMovie(ctx context.Context, lib *ent.Library, path string, info os.FileInfo) error {
	existing, err := s.existingByPath(ctx, path)
	if err != nil {
		return err
	}
	if unchanged(existing, info) {
		return nil
	}

	parsed := ParseMovie(path)
	probed := s.probeFile(ctx, path)
	// A local NFO, when present, overrides the title parsed from the filename.
	nf := nfo.Movie(path, lib.Path)
	name := parsed.Title
	if nf != nil && nf.Title != "" {
		name = nf.Title
	}

	if existing != nil {
		upd := existing.Update().
			SetName(name).
			SetContainer(containerOf(path)).
			SetRunTimeTicks(probed.RunTimeTicks).
			SetMediaStreams(probed.Streams).
			SetMtime(info.ModTime().UnixNano()).
			SetSize(info.Size())
		if parsed.Year != nil {
			upd = upd.SetProductionYear(*parsed.Year)
		}
		if err := upd.Exec(ctx); err != nil {
			return err
		}
		return s.applyNFO(ctx, existing, nf)
	}

	create := s.client.MediaItem.Create().
		SetKind(mediaitem.KindMovie).
		SetName(name).
		SetSortName(sortKey(name)).
		SetPath(path).
		SetContainer(containerOf(path)).
		SetRunTimeTicks(probed.RunTimeTicks).
		SetMediaStreams(probed.Streams).
		SetMtime(info.ModTime().UnixNano()).
		SetSize(info.Size()).
		SetLibrary(lib)
	if parsed.Year != nil {
		create = create.SetProductionYear(*parsed.Year)
	}
	item, err := create.Save(ctx)
	if err != nil {
		return err
	}
	return s.applyNFO(ctx, item, nf)
}
