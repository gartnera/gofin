package scanner

import (
	"context"
	"fmt"
	"os"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/nfo"
)

// indexEpisode indexes a single video file in a tvshows library, deriving its
// Series and Season parents from the parsed metadata.
func (s *Scanner) indexEpisode(ctx context.Context, lib *ent.Library, path string, info os.FileInfo) error {
	existing, err := s.existingByPath(ctx, path)
	if err != nil {
		return err
	}
	if unchanged(existing, info) {
		return nil
	}

	parsed := ParseEpisode(path)
	if !parsed.OK {
		// Not recognisable as an episode; skip rather than mis-file it.
		return nil
	}

	series, err := s.findOrCreateFolder(ctx, lib, mediaitem.KindSeries, parsed.Series, nil)
	if err != nil {
		return fmt.Errorf("series %q: %w", parsed.Series, err)
	}
	// A "tvshow.nfo" in the series directory enriches the Series folder. Only
	// apply it while the folder is still bare so repeated episode scans don't
	// re-write it every time.
	if series.Overview == "" {
		if err := s.applyNFO(ctx, series, nfo.Series(path, lib.Path)); err != nil {
			return err
		}
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
		// Reflect the write on the (possibly cache-shared) struct so sibling
		// episodes in this scan don't re-issue the same update.
		n := parsed.Season
		season.IndexNumber = &n
	}
	if season.Overview == "" {
		if err := s.applyNFO(ctx, season, nfo.Season(path, lib.Path)); err != nil {
			return err
		}
	}

	// A sidecar "<episode>.nfo" overrides the title parsed from the filename.
	nf := nfo.Episode(path)
	title := parsed.Title
	if nf != nil && nf.Title != "" {
		title = nf.Title
	}
	if title == "" {
		if parsed.EndEpisode != nil {
			title = fmt.Sprintf("Episodes %d-%d", parsed.Episode, *parsed.EndEpisode)
		} else {
			title = fmt.Sprintf("Episode %d", parsed.Episode)
		}
	}
	probed := s.probeFile(ctx, path)

	if existing != nil {
		// Always refresh on-disk/probe facts; preserve locked metadata edits.
		upd := existing.Update().
			SetContainer(containerOf(path)).
			SetRunTimeTicks(probed.RunTimeTicks).
			SetMediaStreams(probed.Streams).
			SetMtime(info.ModTime().UnixNano()).
			SetSize(info.Size()).
			SetParentID(season.ID)
		if !metaLocked(existing, "Name") {
			upd = upd.SetName(title)
		}
		if !metaLocked(existing, "IndexNumber") {
			upd = upd.SetIndexNumber(parsed.Episode).
				SetParentIndexNumber(parsed.Season)
			if parsed.EndEpisode != nil {
				upd = upd.SetIndexNumberEnd(*parsed.EndEpisode)
			} else {
				upd = upd.ClearIndexNumberEnd()
			}
		}
		if err := upd.Exec(ctx); err != nil {
			return err
		}
		return s.applyNFO(ctx, existing, nf)
	}

	create := s.client.MediaItem.Create().
		SetKind(mediaitem.KindEpisode).
		SetName(title).
		SetSortName(fmt.Sprintf("%04d", parsed.Episode)).
		SetPath(path).
		SetContainer(containerOf(path)).
		SetRunTimeTicks(probed.RunTimeTicks).
		SetMediaStreams(probed.Streams).
		SetMtime(info.ModTime().UnixNano()).
		SetSize(info.Size()).
		SetIndexNumber(parsed.Episode).
		SetParentIndexNumber(parsed.Season).
		SetLibrary(lib).
		SetParentID(season.ID)
	if parsed.EndEpisode != nil {
		create = create.SetIndexNumberEnd(*parsed.EndEpisode)
	}
	item, err := create.Save(ctx)
	if err != nil {
		return err
	}
	return s.applyNFO(ctx, item, nf)
}
