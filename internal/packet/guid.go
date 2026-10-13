package packet

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// GUID is a 64-bit ObjectGuid; the high 16 bits are the HighGuid (ObjectGuid.h:68-77)
// and, for creatures and game objects, bits 24-47 hold the entry. A pet or guardian
// (HIGHGUID_PET) keeps a pet number there instead (ObjectGuid.cpp:69-70).
type GUID uint64

const (
	HighPlayer     uint16 = 0x0000
	HighGameObject uint16 = 0xF110
	HighTransport  uint16 = 0xF120
	HighUnit       uint16 = 0xF130
	HighPet        uint16 = 0xF140
)

func (g GUID) High() uint16       { return uint16(g >> 48) }
func (g GUID) IsPlayer() bool     { return g != 0 && g.High() == HighPlayer }
func (g GUID) IsCreature() bool   { h := g.High(); return h == HighUnit || h == HighPet }
func (g GUID) IsGameObject() bool { return g.High() == HighGameObject }
func (g GUID) String() string     { return fmt.Sprintf("0x%016X", uint64(g)) }

// Entry is the entry a creature or game-object GUID encodes, and 0 for every other
// HighGuid, pets included: their bits 24-47 are a pet number. The object's
// OBJECT_FIELD_ENTRY is the truth; this is the fallback while the values are unknown.
func (g GUID) Entry() uint32 {
	switch g.High() {
	case HighUnit, HighGameObject, HighTransport:
		return uint32(g>>24) & 0x00FFFFFF
	}
	return 0
}

// MarshalJSON writes a hex string: JSON numbers lose precision above 2^53.
func (g GUID) MarshalJSON() ([]byte, error) { return json.Marshal(g.String()) }

func (g *GUID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("guid must be a hex string like \"0xF130000000001234\": %w", err)
	}
	v, err := ParseGUID(s)
	if err != nil {
		return err
	}
	*g = v
	return nil
}

// ParseGUID accepts "0x"-prefixed or bare hex.
func ParseGUID(s string) (GUID, error) {
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x"), 16, 64)
	if err != nil {
		return 0, fmt.Errorf("bad guid %q", s)
	}
	return GUID(v), nil
}
