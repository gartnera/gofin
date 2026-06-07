package jellyfin

import (
	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/probe"
	"github.com/google/uuid"
	"github.com/sj14/jellyfin-go/api"
)

// folderKinds are item kinds that act as browsable containers rather than
// playable media.
var folderKinds = map[mediaitem.Kind]bool{
	mediaitem.KindSeries:      true,
	mediaitem.KindSeason:      true,
	mediaitem.KindMusicArtist: true,
	mediaitem.KindMusicAlbum:  true,
}

// IsFolder reports whether items of the given kind are containers.
func IsFolder(k mediaitem.Kind) bool {
	return folderKinds[k]
}

// IsPlayable reports whether items of the given kind have a streamable file.
func IsPlayable(k mediaitem.Kind) bool {
	return !folderKinds[k]
}

// MediaTypeFor returns the Jellyfin MediaType for a playable kind.
func MediaTypeFor(k mediaitem.Kind) api.MediaType {
	switch k {
	case mediaitem.KindAudio:
		return api.MEDIATYPE_AUDIO
	case mediaitem.KindMovie, mediaitem.KindEpisode:
		return api.MEDIATYPE_VIDEO
	default:
		return api.MEDIATYPE_UNKNOWN
	}
}

// MapUser converts an ent.User into a Jellyfin UserDto.
func MapUser(u *ent.User, serverID string) api.UserDto {
	dto := api.NewUserDto()
	id := FormatID(u.ID)
	dto.SetId(id)
	dto.SetName(u.Name)
	dto.SetServerId(serverID)
	dto.SetHasPassword(true)
	dto.SetHasConfiguredPassword(true)
	return *dto
}

// MapItem converts an ent.MediaItem into a Jellyfin BaseItemDto. The item's
// parent edge should be eager-loaded (WithParent) so ParentId is populated.
// ps is the requesting user's play state for the item, or nil.
func MapItem(it *ent.MediaItem, serverID string, ps *ent.PlayState) api.BaseItemDto {
	dto := api.NewBaseItemDto()
	dto.SetId(FormatID(it.ID))
	dto.SetServerId(serverID)
	dto.SetName(it.Name)
	if it.SortName != "" {
		dto.SetSortName(it.SortName)
	}
	kind := api.BaseItemKind(it.Kind)
	dto.SetType(kind)
	dto.SetIsFolder(IsFolder(it.Kind))

	if it.Overview != "" {
		dto.SetOverview(it.Overview)
	}
	if it.ProductionYear != nil {
		dto.SetProductionYear(*it.ProductionYear)
	}
	if it.IndexNumber != nil {
		dto.SetIndexNumber(*it.IndexNumber)
	}
	if it.ParentIndexNumber != nil {
		dto.SetParentIndexNumber(*it.ParentIndexNumber)
	}
	if it.AlbumArtist != "" {
		dto.SetAlbumArtist(it.AlbumArtist)
		dto.SetArtists([]string{it.AlbumArtist})
	}
	if it.Edges.Parent != nil {
		dto.SetParentId(FormatID(it.Edges.Parent.ID))
	}
	if it.ImagePath != "" {
		dto.SetImageTags(map[string]string{"Primary": FormatID(it.ID)})
	}

	if IsPlayable(it.Kind) {
		dto.SetMediaType(MediaTypeFor(it.Kind))
		dto.SetRunTimeTicks(it.RunTimeTicks)
		if it.Path != "" {
			dto.SetPath(it.Path)
		}
		dto.SetMediaSources([]api.MediaSourceInfo{MediaSource(it)})
	}
	dto.SetUserData(UserDataFor(it.ID, it.RunTimeTicks, ps))
	return *dto
}

// UserDataFor builds the per-user UserItemDataDto for an item. runtimeTicks is
// used to compute the played percentage and may be 0 when unknown.
func UserDataFor(itemID uuid.UUID, runtimeTicks int64, ps *ent.PlayState) api.UserItemDataDto {
	ud := api.NewUserItemDataDto()
	ud.SetItemId(FormatID(itemID))
	if ps == nil {
		ud.SetPlayed(false)
		ud.SetPlaybackPositionTicks(0)
		ud.SetPlayCount(0)
		return *ud
	}
	ud.SetPlayed(ps.Played)
	ud.SetPlaybackPositionTicks(ps.PlaybackPositionTicks)
	ud.SetPlayCount(int32(ps.PlayCount))
	if runtimeTicks > 0 && ps.PlaybackPositionTicks > 0 {
		ud.SetPlayedPercentage(float64(ps.PlaybackPositionTicks) / float64(runtimeTicks) * 100)
	}
	if ps.LastPlayedDate != nil {
		ud.SetLastPlayedDate(*ps.LastPlayedDate)
	}
	return *ud
}

// mapStreams converts stored probe streams into Jellyfin MediaStream values.
func mapStreams(streams []probe.Stream) []api.MediaStream {
	out := make([]api.MediaStream, 0, len(streams))
	for _, s := range streams {
		ms := api.NewMediaStream()
		ms.SetIndex(s.Index)
		ms.SetType(api.MediaStreamType(s.Type))
		if s.Codec != "" {
			ms.SetCodec(s.Codec)
		}
		if s.Width > 0 {
			ms.SetWidth(s.Width)
		}
		if s.Height > 0 {
			ms.SetHeight(s.Height)
		}
		if s.Channels > 0 {
			ms.SetChannels(s.Channels)
		}
		if s.SampleRate > 0 {
			ms.SetSampleRate(s.SampleRate)
		}
		if s.BitRate > 0 {
			ms.SetBitRate(int32(s.BitRate))
		}
		if s.Language != "" {
			ms.SetLanguage(s.Language)
		}
		ms.SetIsDefault(s.IsDefault)
		out = append(out, *ms)
	}
	return out
}

// MediaSource builds the single direct-play MediaSourceInfo for a playable item.
func MediaSource(it *ent.MediaItem) api.MediaSourceInfo {
	ms := api.NewMediaSourceInfo()
	id := FormatID(it.ID)
	ms.SetId(id)
	ms.SetProtocol(api.MEDIAPROTOCOL_FILE)
	if it.Path != "" {
		ms.SetPath(it.Path)
	}
	if it.Container != "" {
		ms.SetContainer(it.Container)
	}
	ms.SetName(it.Name)
	ms.SetRunTimeTicks(it.RunTimeTicks)
	ms.SetIsRemote(false)
	ms.SetSupportsDirectPlay(true)
	ms.SetSupportsDirectStream(true)
	ms.SetSupportsTranscoding(false)
	if len(it.MediaStreams) > 0 {
		ms.SetMediaStreams(mapStreams(it.MediaStreams))
	}
	return *ms
}

// MapLibraryView converts a Library into a CollectionFolder BaseItemDto for the
// /UserViews response.
func MapLibraryView(lib *ent.Library, serverID string) api.BaseItemDto {
	dto := api.NewBaseItemDto()
	dto.SetId(FormatID(lib.ID))
	dto.SetServerId(serverID)
	dto.SetName(lib.Name)
	dto.SetType(api.BASEITEMKIND_COLLECTION_FOLDER)
	dto.SetIsFolder(true)
	switch lib.Type {
	case "movies":
		dto.SetCollectionType(api.COLLECTIONTYPE_MOVIES)
	case "tvshows":
		dto.SetCollectionType(api.COLLECTIONTYPE_TVSHOWS)
	case "music":
		dto.SetCollectionType(api.COLLECTIONTYPE_MUSIC)
	}
	return *dto
}

// QueryResult wraps items in a BaseItemDtoQueryResult. total is the number of
// records available before paging; startIndex is the offset of the first item.
func QueryResult(items []api.BaseItemDto, total, startIndex int) api.BaseItemDtoQueryResult {
	res := api.NewBaseItemDtoQueryResult()
	res.SetItems(items)
	res.SetTotalRecordCount(int32(total))
	res.SetStartIndex(int32(startIndex))
	return *res
}
