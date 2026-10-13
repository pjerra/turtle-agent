package objects

// Update-field indices (Objects/UpdateFields.h; OBJECT_END = 0x06).
const (
	FieldGUID        uint16 = 0x00 // OBJECT_FIELD_GUID, 2 words (:29)
	FieldType        uint16 = 0x02 // (:30)
	FieldEntry       uint16 = 0x03 // (:31)
	UnitCharmedBy    uint16 = 0x0A // UNIT_FIELD_CHARMEDBY, 2 words (:74)
	UnitSummonedBy   uint16 = 0x0C // UNIT_FIELD_SUMMONEDBY, 2 words (:75)
	UnitCreatedBy    uint16 = 0x0E // UNIT_FIELD_CREATEDBY, 2 words (:76); hunter pets and totems (Pet.cpp:308, Totem.cpp:162)
	UnitTarget       uint16 = 0x10 // UNIT_FIELD_TARGET, 2 words (:77)
	UnitHealth       uint16 = 0x16 // (:80)
	UnitPower1       uint16 = 0x17 // POWER1..5 (:81-85)
	UnitMaxHealth    uint16 = 0x1C // (:86)
	UnitMaxPower1    uint16 = 0x1D // MAXPOWER1..5 (:87-91)
	UnitLevel        uint16 = 0x22 // (:92)
	UnitFaction      uint16 = 0x23 // UNIT_FIELD_FACTIONTEMPLATE (:93)
	UnitBytes0       uint16 = 0x24 // race, class, gender, power type (:94; Unit.h:455)
	UnitFlags        uint16 = 0x2E // (:97; FIELD_UNIT_FIELD_FLAGS = 46 at :345)
	UnitAura         uint16 = 0x2F // 48 slots of spell ids (:98-99)
	UnitAuraApps     uint16 = 0x71 // UNIT_FIELD_AURAAPPLICATIONS: a byte per slot, stacks - 1 (:108; SpellAuras.cpp:7565-7574)
	UnitMountDisplay uint16 = 0x85 // UNIT_FIELD_MOUNTDISPLAYID, non-zero while mounted (:118)
	UnitBytes1       uint16 = 0x8A // UNIT_FIELD_BYTES_1, byte 0 is the stand state (:123; UnitDefines.h:74-85)
	UnitDynamicFlags uint16 = 0x8F // (:128; FIELD_UNIT_DYNAMIC_FLAGS = 143 at :346)
	UnitChannelSpell uint16 = 0x90 // the spell being channelled (:129)
	UnitNPCFlags     uint16 = 0x93 // (:132; FIELD_UNIT_NPC_FLAGS = 147 at :347)
)

// Player fields (Objects/UpdateFields.h, EPlayerFields; UNIT_END = 0xBC). The owner alone gets them.
const (
	// PlayerComboTarget is PLAYER_FIELD_COMBO_TARGET (:221), 2 words: the guid of the unit the
	// combo points are on. Player::SetComboPoints writes it with the points (Player.cpp:20942-20954).
	PlayerComboTarget uint16 = 0x2CA
	// PlayerFieldBytes is PLAYER_FIELD_BYTES (:259). Byte 1 holds the combo points, 0-5 (Player.cpp:20948);
	// byte 0 holds the track-stealthed flag and byte 3 the honor rank (HonorMgr.cpp:804).
	PlayerFieldBytes uint16 = 0x4C6
)

// Aura slots (Spells/SpellAuraDefines.h:25-26).
const (
	AuraSlots         = 48
	PositiveAuraSlots = 32
)

// Unit flags (Objects/UnitDefines.h:402-441).
const (
	UnitFlagSpawning         uint32 = 0x00000002
	UnitFlagPlayerControlled uint32 = 0x00000008
	UnitFlagImmuneToPlayer   uint32 = 0x00000100
	UnitFlagInCombat         uint32 = 0x00080000
	UnitFlagNotSelectable    uint32 = 0x02000000
)

// Unit dynamic flags (SharedDefines.h:1327).
const DynFlagLootable uint32 = 0x0001

// Type ids (ObjectGuid.h:35-42).
const (
	TypeItem       uint8 = 1
	TypeContainer  uint8 = 2
	TypeUnit       uint8 = 3
	TypePlayer     uint8 = 4
	TypeGameObject uint8 = 5
	TypeDynamic    uint8 = 6 // TYPEID_DYNAMICOBJECT: the area a ground spell leaves (Blizzard, a fire patch)
)

// Item and player inventory fields (Objects/UpdateFields.h).
const (
	ItemStackCount uint16 = 0x0E // ITEM_FIELD_STACK_COUNT, OBJECT_END + 0x08 (:43)
	// PlayerInvSlotHead is PLAYER_FIELD_INV_SLOT_HEAD (:202): the guid of inventory slot i,
	// two words each. The 23 equipment and bag slots run on into the 16 backpack slots,
	// PLAYER_FIELD_PACK_SLOT_1 (:204) = head + 46. The owner alone gets these fields.
	PlayerInvSlotHead uint16 = 0x1E6
	// A bag's slot count and its item guids, two words each (:62-64).
	ContainerNumSlots uint16 = 0x30
	ContainerSlot1    uint16 = 0x32
)

// The backpack is bag INVENTORY_SLOT_BAG_0, slots 23-38; the four bags sit in inventory
// slots 19-22 (Objects/Player.h:583, :612-613, :618-619).
const (
	BackpackBag   uint8 = 255
	BackpackFirst uint8 = 23
	BackpackEnd   uint8 = 39
	BagFirst      uint8 = 19
	BagEnd        uint8 = 23
)

// Game object fields (Objects/UpdateFields.h:283-304, EGameObjectFields; OBJECT_END = 0x06).
const (
	GOCreatedBy uint16 = 0x06 // OBJECT_FIELD_CREATED_BY, 2 words: the summoner of a spawned object
	GODisplayID uint16 = 0x08
	GOFlags     uint16 = 0x09 // GO_FLAG_* (SharedDefines.h:293-299)
	GOState     uint16 = 0x0E // GOState (GameObject.h:601-605)
	GOPosX      uint16 = 0x0F // POS_X..POS_Y..POS_Z, FACING: four floats
	GODynFlags  uint16 = 0x13 // GO_DYNFLAG_LO_* (SharedDefines.h:326-328)
	GOFaction   uint16 = 0x14
	GOTypeID    uint16 = 0x15 // GameobjectTypes (SharedDefines.h:253-284)
	GOLevel     uint16 = 0x16
)

// Game object flags (SharedDefines.h:293-299).
const (
	GOFlagInUse        uint32 = 0x01 // disables interaction while animated
	GOFlagLocked       uint32 = 0x02 // needs a key, spell or event
	GOFlagInteractCond uint32 = 0x04 // cannot interact (condition)
	GOFlagNoInteract   uint32 = 0x10 // players cannot interact
)

// Game object states (GameObject.h:601-605).
const (
	GOStateActive    uint32 = 0 // used: a closed door open
	GOStateReady     uint32 = 1 // ready: a closed door closed
	GOStateActiveAlt uint32 = 2 // used the alternative way
)

// Game object types (SharedDefines.h:253-284).
const (
	GOTypeDoor    uint8 = 0
	GOTypeGeneric uint8 = 5
)

// Dynamic object fields (Objects/UpdateFields.h, EDynamicObjectFields; OBJECT_END = 0x06).
const (
	DynCaster uint16 = 0x06 // DYNAMICOBJECT_CASTER, 2 words
	DynSpell  uint16 = 0x09 // DYNAMICOBJECT_SPELLID
	DynRadius uint16 = 0x0A // a float
	DynPosX   uint16 = 0x0B // POS_X..POS_Z, FACING: four floats
)
