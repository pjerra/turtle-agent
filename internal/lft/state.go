package lft

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// timeout is how long the core keeps a role check or an offer open before it cancels it:
// LFT_ROLECHECK_TIMEOUT and LFT_OFFER_TIMEOUT are both 90 s (LFTQeueue.cpp:13-14).
const timeout = 90 * time.Second

// Phase is where the queue conversation stands.
type Phase string

const (
	PhaseIdle      Phase = "idle"
	PhaseRolecheck Phase = "rolecheck"
	PhaseQueued    Phase = "queued"
	PhaseOffer     Phase = "offer"
	PhaseGrouped   Phase = "grouped"
	PhaseLeft      Phase = "left"
	PhaseError     Phase = "error"
)

// Offer is the pending group of S2C_OFFER_NEW: the instance, the role this player was
// picked for, and the accepted counts the core broadcasts.
type Offer struct {
	Instance string `json:"instance"`
	Role     string `json:"role"`
	Counts   [3]int `json:"counts"`
}

// Listing is one group-finder listing, as BuildListingJson writes it
// (LFTUserGroups.cpp:901-911), with the detail rows folded in.
type Listing struct {
	ID           uint32   `json:"id"`
	Creator      string   `json:"creator"`
	Class        string   `json:"class"`
	Level        int      `json:"level"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Category     int      `json:"category"`
	Limit        [3]int   `json:"limit"`
	NumConfirmed [3]int   `json:"numConfirmed"`
	Signups      []Signup `json:"signups,omitempty"`
}

// Signup is one role row of a listing detail (BuildSignupJson, LFTUserGroups.cpp:916-924)
// with the role index of the S2C_UPDATE_GROUP field it arrived in.
type Signup struct {
	Name      string `json:"name"`
	Class     string `json:"class"`
	Level     int    `json:"level"`
	Confirmed bool   `json:"confirmed"`
	Role      string `json:"role,omitempty"`
}

// Line is one folded addon message, kept for the recent-history view.
type Line struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// State is the group-finder state a session's addon messages describe. Fold every
// S2C_* line the spoof guard accepted, in arrival order.
type State struct {
	Phase             Phase              `json:"phase"`
	Instances         []string           `json:"instances,omitempty"`
	RolecheckSince    time.Time          `json:"rolecheck_since,omitempty"`
	OfferSince        time.Time          `json:"offer_since,omitempty"`
	RolecheckDeadline time.Time          `json:"rolecheck_deadline,omitempty"`
	OfferDeadline     time.Time          `json:"offer_deadline,omitempty"`
	Responses         map[string]string  `json:"responses,omitempty"`
	Offer             *Offer             `json:"offer,omitempty"`
	QueuedSince       int64              `json:"queued_since,omitempty"`
	Counts            [3]int             `json:"counts"`
	LastError         string             `json:"last_error,omitempty"`
	Listings          map[uint32]Listing `json:"listings,omitempty"`
	History           []Line             `json:"history,omitempty"`

	// pending holds a groups list between its start and end lines.
	pending map[uint32]Listing
}

// Fold applies one message to the state. The transition table is the core's:
//
//	ROLECHECK_START                       -> rolecheck, deadline +90 s, instances
//	UPDATE_QUEUE_STATUS pending_rolecheck -> rolecheck
//	UPDATE_QUEUE_STATUS pending_offer     -> offer
//	UPDATE_QUEUE_STATUS queued            -> queued, instances, join time, counts
//	QUEUE_JOINED                          -> queued
//	OFFER_NEW                             -> offer (instance, role), deadline +90 s
//	OFFER_UPDATE_COUNT                    -> counts
//	OFFER_COMPLETE                        -> grouped
//	QUEUE_LEFT                            -> left
//	QUEUE_ERROR                           -> error (reason)
//	ROLECHECK_INFO                        -> Responses[name] = roles
//	GROUPS_LIST_UPDATE                    -> start/end commit, JSON adds
//	UPDATE_GROUP                          -> one listing's details and signup rows
//
// Unknown kinds only enter History.
func (s *State) Fold(m Msg, at time.Time) {
	if s.Phase == "" {
		s.Phase = PhaseIdle
	}
	s.History = append(s.History, Line{At: at, Text: m.Raw})
	if len(s.History) > 50 {
		s.History = s.History[len(s.History)-50:]
	}

	switch m.Kind {
	case "S2C_ROLECHECK_START":
		s.Phase = PhaseRolecheck
		s.RolecheckSince = at
		s.RolecheckDeadline = at.Add(timeout)
		s.Instances = splitInstances(field(m, 0))
		s.Responses = make(map[string]string)
		s.Offer = nil

	case "S2C_ROLECHECK_INFO":
		if s.Responses == nil {
			s.Responses = make(map[string]string)
		}
		if len(m.Fields) >= 2 {
			s.Responses[m.Fields[0]] = m.Fields[1]
		}

	case "S2C_UPDATE_QUEUE_STATUS":
		switch field(m, 0) {
		case "pending_rolecheck":
			s.Phase = PhaseRolecheck
			if s.RolecheckDeadline.IsZero() {
				s.RolecheckSince = at
				s.RolecheckDeadline = at.Add(timeout)
			}
		case "pending_offer":
			s.Phase = PhaseOffer
		case "queued":
			s.Phase = PhaseQueued
			s.Instances = splitInstances(field(m, 1))
			s.QueuedSince = parseInt64(field(m, 2))
			s.Counts = parseCounts(field(m, 4))
		}

	case "S2C_QUEUE_JOINED":
		s.Phase = PhaseQueued
		s.Offer = nil

	case "S2C_OFFER_NEW":
		s.Phase = PhaseOffer
		s.Offer = &Offer{Instance: field(m, 0), Role: field(m, 1)}
		s.OfferSince = at
		s.OfferDeadline = at.Add(timeout)
		s.Counts = [3]int{}

	case "S2C_OFFER_UPDATE_COUNT":
		s.Counts = parseCounts(field(m, 0))
		if s.Offer != nil {
			s.Offer.Counts = s.Counts
		}

	case "S2C_OFFER_COMPLETE":
		s.Phase = PhaseGrouped
		s.Offer = nil

	case "S2C_QUEUE_LEFT":
		s.Phase = PhaseLeft
		s.Offer = nil

	case "S2C_QUEUE_ERROR":
		s.Phase = PhaseError
		s.LastError = field(m, 0)

	case "S2C_GROUPS_LIST_UPDATE":
		s.foldListings(m)

	case "S2C_UPDATE_GROUP":
		s.foldGroupDetails(m)
	}
}

func (s *State) foldListings(m Msg) {
	if len(m.Fields) == 0 {
		return
	}
	switch m.Fields[0] {
	case "start":
		s.pending = make(map[uint32]Listing)
	case "end":
		s.Listings = s.pending
		s.pending = nil
	default:
		if m.JSON == nil {
			return
		}
		var listing Listing
		if json.Unmarshal(m.JSON, &listing) != nil {
			return
		}
		if s.pending == nil {
			s.pending = make(map[uint32]Listing)
		}
		s.pending[listing.ID] = listing
	}
}

func (s *State) foldGroupDetails(m Msg) {
	if len(m.Fields) < 2 {
		return
	}
	id, err := strconv.ParseUint(m.Fields[0], 10, 32)
	if err != nil {
		return
	}
	if s.Listings == nil {
		s.Listings = make(map[uint32]Listing)
	}
	key := uint32(id)
	switch m.Fields[1] {
	case "start":
		if m.JSON == nil {
			return
		}
		var listing Listing
		if json.Unmarshal(m.JSON, &listing) != nil {
			return
		}
		listing.Signups = nil
		s.Listings[key] = listing
	case "1", "2", "3":
		if m.JSON == nil {
			return
		}
		var signup Signup
		if json.Unmarshal(m.JSON, &signup) != nil {
			return
		}
		signup.Role = roleName(m.Fields[1])
		listing := s.Listings[key]
		listing.ID = key
		listing.Signups = append(listing.Signups, signup)
		s.Listings[key] = listing
	}
}

func roleName(index string) string {
	switch index {
	case "1":
		return "t"
	case "2":
		return "h"
	case "3":
		return "d"
	}
	return ""
}

func field(m Msg, i int) string {
	if i < len(m.Fields) {
		return m.Fields[i]
	}
	return ""
}

func splitInstances(s string) []string {
	if s == "" {
		return nil
	}
	var instances []string
	for _, instance := range strings.Split(s, ":") {
		if instance != "" {
			instances = append(instances, instance)
		}
	}
	return instances
}

func parseInt64(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func parseCounts(s string) [3]int {
	var counts [3]int
	for i, part := range strings.Split(s, ":") {
		if i == len(counts) {
			break
		}
		counts[i] = int(parseInt64(part))
	}
	return counts
}
