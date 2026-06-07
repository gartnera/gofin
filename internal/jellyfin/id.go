package jellyfin

import (
	"encoding/hex"

	"github.com/google/uuid"
)

// FormatID renders a UUID in the Jellyfin wire form: 32 lowercase hex chars
// with no dashes.
func FormatID(id uuid.UUID) string {
	return hex.EncodeToString(id[:])
}

// ParseID parses an item id in either the dashed canonical form or the
// 32-char dashless form emitted by FormatID.
func ParseID(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}
