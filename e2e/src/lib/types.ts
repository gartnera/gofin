// Minimal shapes for the subset of the Jellyfin API the scenarios touch. These
// are intentionally partial — only the fields we read are declared.

export interface BaseItem {
  Id: string;
  Name: string;
  Type?: string;
  CollectionType?: string;
  MediaType?: string;
}

export interface QueryResult<T = BaseItem> {
  Items: T[];
  TotalRecordCount: number;
  StartIndex: number;
}

export interface StoredServer {
  Id: string;
  UserId: string;
  AccessToken: string;
  ManualAddress?: string;
  Name?: string;
}

export interface StoredCredentials {
  Servers: StoredServer[];
}

/** Snapshot of the page's first <video>/<audio> element during playback. */
export interface MediaState {
  present: boolean;
  tag?: string;
  src?: string;
  readyState?: number;
  paused?: boolean;
  currentTime?: number;
  error?: number | null;
}
