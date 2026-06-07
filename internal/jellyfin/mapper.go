package jellyfin

import (
	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
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
func MapItem(it *ent.MediaItem, serverID string) api.BaseItemDto {
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
	return *dto
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

// QueryResult wraps items in a BaseItemDtoQueryResult with the total count.
func QueryResult(items []api.BaseItemDto) api.BaseItemDtoQueryResult {
	res := api.NewBaseItemDtoQueryResult()
	res.SetItems(items)
	res.SetTotalRecordCount(int32(len(items)))
	res.SetStartIndex(0)
	return *res
}
