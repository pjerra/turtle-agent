package objects

import "github.com/TortoiseBots/turtle-agent/internal/packet"

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// PlayerQuestLog1 is PLAYER_QUEST_LOG_1_1 (Objects/UpdateFields.h:186): 20 quest log slots
// of 3 words each. The owner alone gets these fields.
const PlayerQuestLog1 uint16 = 0xC6

// QuestLogSize is MAX_QUEST_LOG_SIZE (QuestDef.h:35).
const QuestLogSize = 20

// Quest log slot words (QuestSlotOffsets, Player.h:538-543): the quest id, then four 6-bit
// counters in the low three bytes and the state in the top byte, then the timer.
const (
	questWordID    = 0
	questWordState = 1
	questWords     = 3

	questStateComplete uint32 = 0x01 // QUEST_STATE_COMPLETE, Player.h:550
	questStateFail     uint32 = 0x02
)

// QuestSlot is one entry of the quest log.
type QuestSlot struct {
	Slot     uint8    `json:"slot"`
	ID       uint32   `json:"quest_id"`
	Complete bool     `json:"complete"`
	Failed   bool     `json:"failed"`
	Counts   [4]uint8 `json:"counts"` // kills or items collected per objective
}

// QuestLog lists the quests in player's log, in slot order, from the player's own update
// fields (empty until its create block arrives).
func (s *Store) QuestLog(player packet.GUID) []QuestSlot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.objs[player]
	if !ok {
		return nil
	}
	var out []QuestSlot
	for slot := 0; slot < QuestLogSize; slot++ {
		base := PlayerQuestLog1 + uint16(slot*questWords)
		id := p.values[base+questWordID]
		if id == 0 {
			continue
		}
		st := p.values[base+questWordState]
		q := QuestSlot{Slot: uint8(slot), ID: id, Complete: st>>24&questStateComplete != 0, Failed: st>>24&questStateFail != 0}
		for i := range q.Counts {
			q.Counts[i] = uint8(st >> (6 * i) & 0x3F)
		}
		out = append(out, q)
	}
	return out
}
