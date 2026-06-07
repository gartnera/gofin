package jellyfin

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/probe"
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

	dto := MapItem(it, "server1", nil)

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

func TestMapItemWithStreamsAndUserData(t *testing.T) {
	now := time.Now()
	it := &ent.MediaItem{
		ID:           uuid.New(),
		Kind:         mediaitem.KindEpisode,
		Name:         "Pilot",
		Path:         "/media/ep.mp4",
		Container:    "mp4",
		RunTimeTicks: 1000,
		MediaStreams: []probe.Stream{
			{Index: 0, Type: "Video", Codec: "h264", Width: 1280, Height: 720},
			{Index: 1, Type: "Audio", Codec: "aac", Channels: 2},
		},
	}
	ps := &ent.PlayState{
		Played:                false,
		PlaybackPositionTicks: 500,
		PlayCount:             2,
		LastPlayedDate:        &now,
	}

	dto := MapItem(it, "srv", ps)

	src := dto.GetMediaSources()
	if len(src) != 1 || len(src[0].MediaStreams) != 2 {
		t.Fatalf("expected 2 media streams, got %+v", src)
	}
	if src[0].MediaStreams[0].GetCodec() != "h264" {
		t.Errorf("video codec = %q, want h264", src[0].MediaStreams[0].GetCodec())
	}
	ud := dto.GetUserData()
	if ud.GetPlaybackPositionTicks() != 500 || ud.GetPlayCount() != 2 {
		t.Errorf("unexpected UserData: %+v", ud)
	}
	if ud.GetPlayedPercentage() != 50 {
		t.Errorf("PlayedPercentage = %v, want 50", ud.GetPlayedPercentage())
	}
}

func TestUserDataForNil(t *testing.T) {
	id := uuid.New()
	ud := UserDataFor(id, 1000, nil)
	if ud.GetPlayed() || ud.GetPlaybackPositionTicks() != 0 || ud.GetPlayCount() != 0 {
		t.Errorf("nil play state should yield zero-value UserData, got %+v", ud)
	}
	if ud.GetItemId() != FormatID(id) {
		t.Errorf("ItemId = %q, want %q", ud.GetItemId(), FormatID(id))
	}
}

func TestMapUserIncludesPolicyAndConfiguration(t *testing.T) {
	u := &ent.User{ID: uuid.New(), Name: "demo", IsAdmin: true}
	dto := MapUser(u, "srv")

	// The web client dereferences both unconditionally during startup.
	if !dto.HasPolicy() {
		t.Fatal("UserDto.Policy missing")
	}
	pol := dto.GetPolicy()
	if !pol.GetIsAdministrator() {
		t.Error("Policy.IsAdministrator = false, want true for admin user")
	}
	if !pol.GetEnableMediaPlayback() {
		t.Error("Policy.EnableMediaPlayback = false, want true")
	}

	if !dto.HasConfiguration() {
		t.Fatal("UserDto.Configuration missing")
	}
	cfg := dto.GetConfiguration()
	if cfg.GetSubtitleMode() != api.SUBTITLEPLAYBACKMODE_DEFAULT {
		t.Errorf("Configuration.SubtitleMode = %v, want Default", cfg.GetSubtitleMode())
	}
}

func TestMapItemAudioFillsAlbumAndArtistFields(t *testing.T) {
	artist := &ent.MediaItem{ID: uuid.New(), Kind: mediaitem.KindMusicArtist, Name: "Echo Hill"}
	album := &ent.MediaItem{ID: uuid.New(), Kind: mediaitem.KindMusicAlbum, Name: "First Light"}
	album.Edges.Parent = artist
	track := &ent.MediaItem{
		ID:           uuid.New(),
		Kind:         mediaitem.KindAudio,
		Name:         "Sunrise",
		Path:         "/music/Echo Hill/First Light/01 - Sunrise.opus",
		Container:    "opus",
		RunTimeTicks: 300_000,
		AlbumArtist:  "Echo Hill",
	}
	track.Edges.Parent = album

	dto := MapItem(track, "srv", nil)

	if dto.GetAlbum() != "First Light" {
		t.Errorf("Album = %q, want %q", dto.GetAlbum(), "First Light")
	}
	if dto.GetAlbumId() != FormatID(album.ID) {
		t.Errorf("AlbumId = %q, want %q", dto.GetAlbumId(), FormatID(album.ID))
	}

	// The album-detail page calls track.ArtistItems.map() and
	// track.AlbumArtists.map() unconditionally; both must be present.
	artists := dto.GetArtistItems()
	if len(artists) != 1 || artists[0].GetName() != "Echo Hill" {
		t.Fatalf("ArtistItems = %+v, want one entry named Echo Hill", artists)
	}
	if artists[0].GetId() != FormatID(artist.ID) {
		t.Errorf("ArtistItems[0].Id = %q, want %q", artists[0].GetId(), FormatID(artist.ID))
	}
	albumArtists := dto.GetAlbumArtists()
	if len(albumArtists) != 1 || albumArtists[0].GetName() != "Echo Hill" {
		t.Fatalf("AlbumArtists = %+v, want one entry named Echo Hill", albumArtists)
	}
}

func TestMapItemAudioWithoutGrandparentLeavesArtistIdEmpty(t *testing.T) {
	// When the query didn't eager-load the grandparent the Id will be unset,
	// but the entries themselves must still be present so the web client's
	// unconditional .map() succeeds.
	album := &ent.MediaItem{ID: uuid.New(), Kind: mediaitem.KindMusicAlbum, Name: "First Light"}
	track := &ent.MediaItem{
		ID:          uuid.New(),
		Kind:        mediaitem.KindAudio,
		Name:        "Sunrise",
		Path:        "/music/x.opus",
		AlbumArtist: "Echo Hill",
	}
	track.Edges.Parent = album

	dto := MapItem(track, "srv", nil)
	artists := dto.GetArtistItems()
	if len(artists) != 1 {
		t.Fatalf("ArtistItems len = %d, want 1", len(artists))
	}
	if artists[0].GetId() != "" {
		t.Errorf("ArtistItems[0].Id = %q, want empty without grandparent", artists[0].GetId())
	}
}

func TestQueryResultAlwaysEmitsItemsArray(t *testing.T) {
	// The SDK marks Items as omitempty, which would crash the web client's
	// `response.Items.length` read. QueryResult must defeat that.
	for name, in := range map[string][]api.BaseItemDto{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			r := QueryResult(in, 0, 0)
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			items, ok := m["Items"]
			if !ok {
				t.Fatalf("Items key missing from %s: %s", name, b)
			}
			if _, ok := items.([]any); !ok {
				t.Errorf("Items = %v (%T), want []any", items, items)
			}
		})
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

	dto := MapItem(it, "server1", nil)

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
