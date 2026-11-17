package session

import (
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// npcState is what the session remembers of its dealings with vendors, trainers and quest
// givers. Owner loop only.
type npcState struct {
	buys      []pendingBuy
	sells     []pendingSell
	accepts   []pendingQuest
	abandons  []pendingQuest
	completes map[questKey]time.Time // quest_complete asked, the hand-in not yet pressed through
	offers    map[questKey]offer     // the last offer from that NPC for that quest
	rewarding map[uint32]rewardAsk   // quest id to the NPC its reward was asked of
}

// offer is a quest_offer_reward seen: how many reward choices it had, kept for offerKeep.
type offer struct {
	choices int
	expires time.Time
}

// rewardAsk is the NPC a quest_reward was sent to, kept until the reward arrives or npcWait ends.
type rewardAsk struct {
	npc     packet.GUID
	expires time.Time
}

// offerKeep is how long an offer is remembered for checking a quest_reward against it.
const offerKeep = 10 * time.Minute

func newNPCState() npcState {
	return npcState{completes: map[questKey]time.Time{}, offers: map[questKey]offer{}, rewarding: map[uint32]rewardAsk{}}
}

// registerNPC installs the vendor, trainer and quest handlers.
func (s *Session) registerNPC() {
	s.registerVendor()
	s.registerQuests()
}

// checkNPC looks for the evidence the pending sales, accepts and abandons wait for. It runs
// after every packet, since the core answers those requests with changed update fields.
func (s *Session) checkNPC() {
	// SMSG_NEW_WORLD clears the store: until the player's own object is back, an empty slot or
	// an empty quest log is "unknown", not "gone".
	if len(s.npc.sells) == 0 && len(s.npc.accepts) == 0 && len(s.npc.abandons) == 0 {
		return
	}
	if _, ok := s.store.Money(s.selfGUID()); !ok {
		return
	}
	if len(s.npc.sells) > 0 {
		s.checkSells()
	}
	if len(s.npc.accepts) > 0 || len(s.npc.abandons) > 0 {
		s.checkQuestLog()
	}
}

// prunePending forgets requests whose evidence never came.
func (s *Session) prunePending(now time.Time) {
	n := &s.npc
	buys := n.buys[:0]
	for _, p := range n.buys {
		if now.Before(p.expires) {
			buys = append(buys, p)
		}
	}
	n.buys = buys
	sells := n.sells[:0]
	for _, p := range n.sells {
		if now.Before(p.expires) {
			sells = append(sells, p)
		}
	}
	n.sells = sells
	for _, list := range []*[]pendingQuest{&n.accepts, &n.abandons} {
		kept := (*list)[:0]
		for _, p := range *list {
			if now.Before(p.expires) {
				kept = append(kept, p)
			}
		}
		*list = kept
	}
	for k, until := range n.completes {
		if !now.Before(until) {
			delete(n.completes, k)
		}
	}
	for k, o := range n.offers {
		if !now.Before(o.expires) {
			delete(n.offers, k)
		}
	}
	for q, a := range n.rewarding {
		if !now.Before(a.expires) {
			delete(n.rewarding, q)
		}
	}
}
