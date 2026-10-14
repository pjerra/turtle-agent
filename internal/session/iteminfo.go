package session

import (
	"context"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
)

// Item prototypes by entry, from CMSG_ITEM_QUERY_SINGLE, as a client asks for the tooltip of an item it does not know (ItemHandler.cpp:336).
// The pre-run gear check of a hired party reads the quality and item level of what the hires wear with it. The cache holds answers for the
// life of the session (a prototype does not change); an entry the core did not know is cached as not found.

type itemCache struct {
	mu    sync.Mutex
	items map[uint32]game.ItemInfo
}

func (c *itemCache) get(entry uint32) (game.ItemInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[entry]
	return it, ok
}

func (c *itemCache) set(it game.ItemInfo) {
	c.mu.Lock()
	if c.items == nil {
		c.items = map[uint32]game.ItemInfo{}
	}
	c.items[it.Entry] = it
	c.mu.Unlock()
}

func (s *Session) registerItemInfo() {
	s.handlers[opcode.SMSGItemQuerySingleResponse] = s.onItemQueryResponse
}

func (s *Session) onItemQueryResponse(body []byte) error {
	it, err := game.ParseItemQueryResponse(body)
	if err != nil {
		return err
	}
	s.itemInfo.set(it)
	return nil
}

// itemPoll is how often ItemInfos looks at the cache for an answer.
const itemPoll = 20 * time.Millisecond

// ItemInfos returns the prototypes of the entries. The ones not cached are asked for in one go; the call waits up to wait for all the
// answers and returns what came (an entry the server never answered is missing from the map, one it does not know has Found false).
func (s *Session) ItemInfos(ctx context.Context, entries []uint32, wait time.Duration) (map[uint32]game.ItemInfo, error) {
	out := map[uint32]game.ItemInfo{}
	var missing []uint32
	seen := map[uint32]bool{}
	for _, e := range entries {
		if e == 0 || seen[e] {
			continue
		}
		seen[e] = true
		if it, ok := s.itemInfo.get(e); ok {
			out[e] = it
		} else {
			missing = append(missing, e)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	if _, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		for _, e := range missing {
			if err := s.conn.WritePacket(opcode.CMSGItemQuerySingle, game.BuildItemQuery(e)); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}); err != nil {
		return out, err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	tick := time.NewTicker(itemPoll)
	defer tick.Stop()
	for {
		left := missing[:0:0]
		for _, e := range missing {
			if it, ok := s.itemInfo.get(e); ok {
				out[e] = it
			} else {
				left = append(left, e)
			}
		}
		missing = left
		if len(missing) == 0 {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-timer.C:
			return out, nil
		case <-tick.C:
		}
	}
}
