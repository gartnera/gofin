package jellyfin

import (
	"testing"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/google/uuid"
	"github.com/sj14/jellyfin-go/api"
)

func TestFormatAndParseID(t *testing.T) {
	id := uuid.New()
	formatted := FormatID(id)
	if len(formatted) != 32 {
		t.Fatalf("FormatID length = %d, want 32", len(formatted))
	}
	parsed, err := ParseID(formatted)
	if err != nil {
		t.Fatalf("ParseID(%q): %v", formatted, err)
	}
	if parsed != id {
		t.Errorf("round-trip mismatch: %v != %v", parsed, id)
	}
}

func TestMapItemMovie(t *testing.T) {
	year := int32(2010)
	it := &ent.MediaItem{
		ID:             uuid.New(),
		Kind:           mediaitem.KindMovie,
		Name:           "Inception",
		Path:           "/media/Inception (2010).mp4",
		Container:      "mp4",
		RunTimeTicks:   123,
		ProductionYear: &year,
	}

	dto := MapItem(it, "server1")

	if dto.GetId() != FormatID(it.ID) {
		t.Errorf("Id = %q, want %q", dto.GetId(), FormatID(it.ID))
	}
	if dto.GetType() != api.BASEITEMKIND_MOVIE {
		t.Errorf("Type = %v, want Movie", dto.GetType())
	}
	if dto.GetIsFolder() {
		t.Error("Movie should not be a folder")
	}
	if dto.GetMediaType() != api.MEDIATYPE_VIDEO {
		t.Errorf("MediaType = %v, want Video", dto.GetMediaType())
	}
	if dto.GetProductionYear() != 2010 {
		t.Errorf("ProductionYear = %d, want 2010", dto.GetProductionYear())
	}
	sources := dto.GetMediaSources()
	if len(sources) != 1 {
		t.Fatalf("MediaSources count = %d, want 1", len(sources))
	}
	if !sources[0].GetSupportsDirectPlay() {
		t.Error("MediaSource should support direct play")
	}
}

func TestMapItemFolderWithParent(t *testing.T) {
	parent := &ent.MediaItem{ID: uuid.New(), Kind: mediaitem.KindSeries, Name: "Show"}
	it := &ent.MediaItem{
		ID:   uuid.New(),
		Kind: mediaitem.KindSeason,
		Name: "Season 1",
	}
	it.Edges.Parent = parent

	dto := MapItem(it, "server1")

	if !dto.GetIsFolder() {
		t.Error("Season should be a folder")
	}
	if len(dto.GetMediaSources()) != 0 {
		t.Error("folder should not have media sources")
	}
	if dto.GetParentId() != FormatID(parent.ID) {
		t.Errorf("ParentId = %q, want %q", dto.GetParentId(), FormatID(parent.ID))
	}
}
