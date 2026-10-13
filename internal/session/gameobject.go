package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5d79486.

func (s *Session) registerGameObjects() {
	s.handlers[opcode.SMSGGameobjectCustomAnim] = func(b []byte) error { return s.onGameObjectAnim(b, "animation", true) }
	s.handlers[opcode.SMSGGameobjectResetState] = func(b []byte) error { return s.onGameObjectAnim(b, "reset", false) }
	s.handlers[opcode.SMSGGameobjectSpawnAnim] = func(b []byte) error { return s.onGameObjectAnim(b, "spawn", false) }
	s.handlers[opcode.SMSGGameobjectDespawnAnim] = func(b []byte) error { return s.onGameObjectAnim(b, "despawn", false) }
}

// GameObjects snapshots the game objects around the player, nearest first, with names filled in
// from the query responses. Like Units, the store is asked without a limit so the name filter sees
// past the nearest 100.
func (s *Session) GameObjects(f objects.GOFilter) []objects.GameObject {
	x, y, z, _ := s.selfPos()
	name := strings.ToLower(f.Name)
	limit := f.Limit
	if limit <= 0 {
		limit = objects.DefaultLimit
	}
	f.Name, f.Limit = "", math.MaxInt
	var out []objects.GameObject
	for _, g := range s.store.GameObjects(f, x, y, z) {
		g.Name, _ = s.names.lookup(g.GUID, g.Entry)
		if name != "" && !strings.Contains(strings.ToLower(g.Name), name) {
			continue
		}
		out = append(out, g)
		if len(out) == limit {
			break
		}
	}
	return out
}

// UseObject uses a game object the way a right-click does (CMSG_GAMEOBJ_USE) and returns it as
// the store holds it. The object must be in view: the client cannot click what it has not been
// sent. The core answers nothing when the object is out of reach, generic, flagged no-interact or
// refuses the player (SpellHandler.cpp:286-318), so the caller reads the outcome from the events.
func (s *Session) UseObject(ctx context.Context, g packet.GUID) (objects.GameObject, error) {
	if g == 0 {
		return objects.GameObject{}, errors.New("use_object needs a guid")
	}
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		x, y, z, _ := s.selfPos()
		o, ok := s.store.GameObject(g, x, y, z)
		if !ok {
			return nil, fmt.Errorf("game object %v is not in view", g)
		}
		o.Name, _ = s.names.lookup(g, o.Entry)
		return o, s.conn.WritePacket(opcode.CMSGGameobjUse, game.BuildGameObjectUse(g))
	})
	if err != nil {
		return objects.GameObject{}, err
	}
	return v.(objects.GameObject), nil
}

// emitGameObjectChange logs a state, flags or dyn_flags change of a game object the store already held.
func (s *Session) emitGameObjectChange(c objects.Change) {
	d := map[string]any{}
	switch c.Kind {
	case objects.ChangeGOState:
		d["change"], d["old"], d["new"] = "state", objects.GOStateName(c.Old), objects.GOStateName(c.New)
	case objects.ChangeGOFlags:
		d["change"], d["old"], d["new"] = "flags", c.Old, c.New
		d["locked"] = c.New&objects.GOFlagLocked != 0
	case objects.ChangeGODynFlags:
		d["change"], d["old"], d["new"] = "dyn_flags", c.Old, c.New
	}
	s.emitGameObject(c.GUID, "", d)
}

// emitGameObject logs a gameobject event for g. data may carry a change already; change names it
// otherwise. The entry and type of the object go along, so a reader need not look it up.
func (s *Session) emitGameObject(g packet.GUID, change string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	if change != "" {
		data["change"] = change
	}
	data["entry"] = s.store.Entry(g)
	data["type"] = objects.GOTypeName(uint8(s.store.Field(g, objects.GOTypeID)))
	s.emit(events.Event{Kind: events.KindGameObject, SourceGUID: g, Data: data})
}

// onGameObjectAnim logs the animation packets of a game object: the custom animation of a door or
// lever (SMSG_GAMEOBJECT_CUSTOM_ANIM, data.anim), its reset, its spawn and its despawn.
func (s *Session) onGameObjectAnim(body []byte, change string, custom bool) error {
	a, err := game.ParseGameObjectAnim(body, custom)
	if err != nil {
		return err
	}
	d := map[string]any{}
	if custom {
		d["anim"] = a.Anim
	}
	s.emitGameObject(a.GUID, change, d)
	return nil
}
