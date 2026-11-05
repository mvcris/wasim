package store

import (
	"encoding/base64"
	"strconv"
)

// EncodeCursor turns a keyset position into an opaque paging cursor.
func EncodeCursor(seq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(seq, 10)))
}

// DecodeCursor reverses EncodeCursor.
func DecodeCursor(cursor string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(string(raw), 10, 64)
}
