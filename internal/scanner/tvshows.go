package scanner

import (
	"context"
	"fmt"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
)

// indexEpisode indexes a single video file in a tvshows library, deriving its
// Series and Season parents from the parsed metadata.
func (s *Scanner) indexEpisode(ctx context.Context, lib *ent.Library, path string) error {
	parsed := ParseEpisode(path)
	if !parsed.OK {
		// Not recognisable as an episode; skip rather than mis-file it.
		return nil
	}

	series, err := s.findOrCreateFolder(ctx, lib, mediaitem.KindSeries, parsed.Series, nil)
	if err != nil {
		return fmt.Errorf("series %q: %w", parsed.Series, err)
	}
	seasonName := fmt.Sprintf("Season %d", parsed.Season)
	season, err := s.findOrCreateFolder(ctx, lib, mediaitem.KindSeason, seasonName, &series.ID)
	if err != nil {
		return fmt.Errorf("season %q: %w", seasonName, err)
	}
	if season.IndexNumber == nil {
		if err := season.Update().SetIndexNumber(parsed.Season).Exec(ctx); err != nil {
			return err
		}
	}

	title := parsed.Title
	if title == "" {
		title = fmt.Sprintf("Episode %d", parsed.Episode)
	}

	existing, err := s.existingByPath(ctx, path)
	if err != nil {
		return err
	}
	if existing != nil {
		return existing.Update().
			SetName(title).
			SetContainer(containerOf(path)).
			SetIndexNumber(parsed.Episode).
			SetParentIndexNumber(parsed.Season).
			SetParentID(season.ID).
			Exec(ctx)
	}

	return s.client.MediaItem.Create().
		SetKind(mediaitem.KindEpisode).
		SetName(title).
		SetSortName(fmt.Sprintf("%04d", parsed.Episode)).
		SetPath(path).
		SetContainer(containerOf(path)).
		SetIndexNumber(parsed.Episode).
		SetParentIndexNumber(parsed.Season).
		SetLibrary(lib).
		SetParentID(season.ID).
		Exec(ctx)
}
