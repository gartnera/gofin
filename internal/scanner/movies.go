package scanner

import (
	"context"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
)

// indexMovie indexes a single video file in a movies library as a Movie.
func (s *Scanner) indexMovie(ctx context.Context, lib *ent.Library, path string) error {
	parsed := ParseMovie(path)

	existing, err := s.existingByPath(ctx, path)
	if err != nil {
		return err
	}
	if existing != nil {
		upd := existing.Update().
			SetName(parsed.Title).
			SetContainer(containerOf(path))
		if parsed.Year != nil {
			upd = upd.SetProductionYear(*parsed.Year)
		}
		return upd.Exec(ctx)
	}

	create := s.client.MediaItem.Create().
		SetKind(mediaitem.KindMovie).
		SetName(parsed.Title).
		SetSortName(sortKey(parsed.Title)).
		SetPath(path).
		SetContainer(containerOf(path)).
		SetLibrary(lib)
	if parsed.Year != nil {
		create = create.SetProductionYear(*parsed.Year)
	}
	return create.Exec(ctx)
}
