package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dhowden/tag"
	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/nfo"
)

// trackMeta is the metadata used to place an audio file in the hierarchy.
type trackMeta struct {
	Artist string
	Album  string
	Title  string
	Track  *int32
}

// readTrackMeta reads embedded tags from an audio file, falling back to
// path-derived names when tags are missing.
func readTrackMeta(path string) trackMeta {
	m := trackMeta{}
	if f, err := os.Open(path); err == nil {
		defer f.Close()
		if md, err := tag.ReadFrom(f); err == nil {
			m.Artist = firstNonEmpty(md.AlbumArtist(), md.Artist())
			m.Album = md.Album()
			m.Title = md.Title()
			if n, _ := md.Track(); n > 0 {
				t := int32(n)
				m.Track = &t
			}
		}
	}
	if m.Artist == "" {
		m.Artist = cleanName(filepath.Base(filepath.Dir(filepath.Dir(path))))
	}
	if m.Artist == "" {
		m.Artist = "Unknown Artist"
	}
	if m.Album == "" {
		m.Album = cleanName(filepath.Base(filepath.Dir(path)))
	}
	if m.Album == "" {
		m.Album = "Unknown Album"
	}
	if m.Title == "" {
		m.Title = cleanName(baseNoExt(path))
	}
	return m
}

// indexAudio indexes an audio file in a music library, deriving its
// MusicArtist and MusicAlbum parents.
func (s *Scanner) indexAudio(ctx context.Context, lib *ent.Library, path string, info os.FileInfo) error {
	existing, err := s.existingByPath(ctx, path)
	if err != nil {
		return err
	}
	if unchanged(existing, info) {
		return nil
	}

	meta := readTrackMeta(path)

	artist, err := s.findOrCreateFolder(ctx, lib, mediaitem.KindMusicArtist, meta.Artist, nil)
	if err != nil {
		return fmt.Errorf("artist %q: %w", meta.Artist, err)
	}
	// "artist.nfo" / "album.nfo" enrich the folder rows; apply only while still
	// bare so repeated track scans don't re-write them.
	if artist.Overview == "" {
		if err := s.applyNFO(ctx, artist, nfo.Artist(path, lib.Path)); err != nil {
			return err
		}
	}
	album, err := s.findOrCreateFolder(ctx, lib, mediaitem.KindMusicAlbum, meta.Album, &artist.ID)
	if err != nil {
		return fmt.Errorf("album %q: %w", meta.Album, err)
	}
	if album.AlbumArtist == "" {
		if err := album.Update().SetAlbumArtist(meta.Artist).Exec(ctx); err != nil {
			return err
		}
	}
	if album.Overview == "" {
		if err := s.applyNFO(ctx, album, nfo.Album(path, lib.Path)); err != nil {
			return err
		}
	}

	// A sidecar "<track>.nfo" overrides the title derived from tags/filename.
	nf := nfo.Track(path)
	title := meta.Title
	if nf != nil && nf.Title != "" {
		title = nf.Title
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
			SetAlbumArtist(meta.Artist).
			SetParentID(album.ID)
		if !metaLocked(existing, "Name") {
			upd = upd.SetName(title)
		}
		if meta.Track != nil && !metaLocked(existing, "IndexNumber") {
			upd = upd.SetIndexNumber(*meta.Track)
		}
		if err := upd.Exec(ctx); err != nil {
			return err
		}
		return s.applyNFO(ctx, existing, nf)
	}

	create := s.client.MediaItem.Create().
		SetKind(mediaitem.KindAudio).
		SetName(title).
		SetSortName(sortKey(title)).
		SetPath(path).
		SetContainer(containerOf(path)).
		SetRunTimeTicks(probed.RunTimeTicks).
		SetMediaStreams(probed.Streams).
		SetMtime(info.ModTime().UnixNano()).
		SetSize(info.Size()).
		SetAlbumArtist(meta.Artist).
		SetLibrary(lib).
		SetParentID(album.ID)
	if meta.Track != nil {
		create = create.SetIndexNumber(*meta.Track)
	}
	item, err := create.Save(ctx)
	if err != nil {
		return err
	}
	return s.applyNFO(ctx, item, nf)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
