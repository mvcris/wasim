package cloudapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// Direction byte and key length of the undocumented wamid layout, taken from
// the samples in Meta's webhook reference. Consumers do parse wamids in the
// wild (to match the two forms a quoted message can arrive under), so a random
// string would hide bugs that real ids expose.
const (
	wamidOutboundTag    = 0x11
	wamidInboundTag     = 0x12
	wamidOutboundKeyLen = 18
	wamidInboundKeyLen  = 20
)

const wamidPrefix = "wamid."

// NewOutboundWAMID returns a wamid for a message the business sent to waID.
func NewOutboundWAMID(waID string) string {
	return encodeWAMID(waID, wamidOutboundTag, randomHexKey(wamidOutboundKeyLen))
}

// NewInboundWAMID returns a wamid for a message waID sent to the business.
func NewInboundWAMID(waID string) string {
	return encodeWAMID(waID, wamidInboundTag, randomHexKey(wamidInboundKeyLen))
}

func encodeWAMID(waID string, directionTag byte, key string) string {
	raw := make([]byte, 0, len(waID)+len(key)+10)
	// Recipient ids have at most 15 digits and keys are fixed short lengths, so
	// both lengths fit the format's single byte.
	raw = append(raw, 0x1C, 0x18, byte(len(waID))) //nolint:gosec // see above
	raw = append(raw, waID...)
	raw = append(raw, 0x15, 0x02, 0x00, directionTag, 0x18, byte(len(key))) //nolint:gosec // see above
	raw = append(raw, key...)
	raw = append(raw, 0x00)
	return wamidPrefix + base64.StdEncoding.EncodeToString(raw)
}

func randomHexKey(length int) string {
	raw := make([]byte, length/2)
	_, _ = rand.Read(raw)
	return strings.ToUpper(hex.EncodeToString(raw))
}
