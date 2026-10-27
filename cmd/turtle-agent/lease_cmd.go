// Command lease manages one-slot-per-worker leases over the slot table
// (internal/slots): acquire, renew, release and list.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/pjerra/turtle-agent/internal/slots"
)

// exitSlotRefused is the exit code of a lease/setup refusal (exit 15).
const exitSlotRefused = 15

const leaseUsage = "usage: turtle-agent lease acquire <server> (--slot ID | --any) --holder NAME [--ttl MIN] [--slots FILE] | renew <server> <slot> --holder NAME [--ttl MIN] [--slots FILE] | release <server> <slot> --holder NAME [--force] [--slots FILE] | list [<server>] [--json] [--slots FILE]"

var leaseHolderRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// splitPositionals pulls leading non-flag args off the front so forms like
// `lease acquire <server> --slot 1` work with the stdlib flag package (which
// stops parsing at the first positional).
func splitPositionals(args []string, n int) (pos []string, rest []string) {
	for len(args) > 0 && n > 0 && !isFlagArg(args[0]) {
		pos = append(pos, args[0])
		args = args[1:]
		n--
	}
	return pos, args
}

func isFlagArg(s string) bool {
	return len(s) > 1 && s[0] == '-' && s != "-"
}
func leaseCmd(args []string, stdout, stderr io.Writer) int {
	if !slotsSupported() {
		return slotsUnsupported("lease", stderr)
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, leaseUsage)
		return exitUsage
	}
	switch args[0] {
	case "acquire":
		return leaseAcquireCmd(args[1:], stdout, stderr)
	case "renew":
		return leaseRenewCmd(args[1:], stdout, stderr)
	case "release":
		return leaseReleaseCmd(args[1:], stdout, stderr)
	case "list":
		return leaseListCmd(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, leaseUsage)
		return exitUsage
	}
}

func leaseStore(slotsPath string) (*slots.Config, *slots.Store, error) {
	if slotsPath == "" {
		slotsPath = slots.DefaultPath()
	}
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		return nil, nil, err
	}
	return cfg, slots.NewStore(cfg, slotsPath), nil
}

func leaseTTL(ttl int) (time.Duration, bool) {
	if ttl < 1 || ttl > 720 {
		return 0, false
	}
	return time.Duration(ttl) * time.Minute, true
}

func leaseAcquireCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lease acquire", flag.ContinueOnError)
	fs.SetOutput(stderr)
	slotID := fs.String("slot", "", "slot id to take")
	any := fs.Bool("any", false, "take the first free slot of the server")
	holder := fs.String("holder", "", "lease holder name")
	ttlMin := fs.Int("ttl", 180, "lease length in minutes (1-720)")
	slotsPath := fs.String("slots", "", "slot table file")
	pos, rest := splitPositionals(args, 1)
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if len(pos) != 1 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, leaseUsage)
		return exitUsage
	}
	server := pos[0]
	if (*slotID == "") == (!*any) {
		fmt.Fprintln(stderr, "lease acquire: pass --slot ID or --any")
		return exitUsage
	}
	if !leaseHolderRe.MatchString(*holder) {
		fmt.Fprintln(stderr, "lease: --holder must be 1-64 letters, digits, . _ -")
		return exitUsage
	}
	ttl, ok := leaseTTL(*ttlMin)
	if !ok {
		fmt.Fprintf(stderr, "lease: --ttl %d: 1 to 720 minutes\n", *ttlMin)
		return exitUsage
	}
	_, store, err := leaseStore(*slotsPath)
	if err != nil {
		fmt.Fprintf(stderr, "lease: %v\n", err)
		return exitUsage
	}
	got, err := store.Acquire(server, *slotID, *any, *holder, ttl)
	if err != nil {
		if r, ok := err.(*slots.Refusal); ok {
			fmt.Fprintf(stderr, "lease: %s\n", r.Msg)
			return exitSlotRefused
		}
		fmt.Fprintf(stderr, "lease: %v\n", err)
		return exitUsage
	}
	if got.TookOver != nil {
		old := got.TookOver
		agent := "no agent running"
		if st, lerr := store.List(server); lerr == nil {
			for _, s := range st {
				if s.Slot.ID == got.Lease.Slot && s.Lease != nil && s.Lease.Holder == got.Lease.Holder {
					if s.AgentAlive {
						agent = fmt.Sprintf("agent pid %d running", s.AgentPID)
					}
					break
				}
			}
		}
		fmt.Fprintf(stdout, "took over a stale lease of %s/%s from %s (expired %s, %s)\n",
			old.Server, old.Slot, old.Holder, old.Expires.UTC().Format("15:04:05Z"), agent)
	}
	fmt.Fprintf(stdout, "leased %s/%s to %s until %s: account %s, character %s, port %d",
		got.Lease.Server, got.Lease.Slot, got.Lease.Holder,
		got.Lease.Expires.UTC().Format("15:04:05Z"),
		got.Slot.Account, got.Slot.Character, got.Slot.Port)
	if got.Renewed {
		fmt.Fprint(stdout, " (already yours: expiry renewed)")
	}
	fmt.Fprintln(stdout)
	return exitOK
}

func leaseRenewCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lease renew", flag.ContinueOnError)
	fs.SetOutput(stderr)
	holder := fs.String("holder", "", "lease holder name")
	ttlMin := fs.Int("ttl", 180, "lease length in minutes (1-720)")
	slotsPath := fs.String("slots", "", "slot table file")
	pos, rest := splitPositionals(args, 2)
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if len(pos) != 2 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, leaseUsage)
		return exitUsage
	}
	server, slotID := pos[0], pos[1]
	if !leaseHolderRe.MatchString(*holder) {
		fmt.Fprintln(stderr, "lease: --holder must be 1-64 letters, digits, . _ -")
		return exitUsage
	}
	ttl, ok := leaseTTL(*ttlMin)
	if !ok {
		fmt.Fprintf(stderr, "lease: --ttl %d: 1 to 720 minutes\n", *ttlMin)
		return exitUsage
	}
	_, store, err := leaseStore(*slotsPath)
	if err != nil {
		fmt.Fprintf(stderr, "lease: %v\n", err)
		return exitUsage
	}
	l, err := store.Renew(server, slotID, *holder, ttl)
	if err != nil {
		if r, ok := err.(*slots.Refusal); ok {
			fmt.Fprintf(stderr, "lease: %s\n", r.Msg)
			return exitSlotRefused
		}
		fmt.Fprintf(stderr, "lease: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "renewed %s/%s for %s until %s\n",
		l.Server, l.Slot, l.Holder, l.Expires.UTC().Format("15:04:05Z"))
	return exitOK
}

func leaseReleaseCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lease release", flag.ContinueOnError)
	fs.SetOutput(stderr)
	holder := fs.String("holder", "", "lease holder name")
	force := fs.Bool("force", false, "release another holder's or a live lease")
	slotsPath := fs.String("slots", "", "slot table file")
	pos, rest := splitPositionals(args, 2)
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if len(pos) != 2 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, leaseUsage)
		return exitUsage
	}
	server, slotID := pos[0], pos[1]
	if !leaseHolderRe.MatchString(*holder) {
		fmt.Fprintln(stderr, "lease: --holder must be 1-64 letters, digits, . _ -")
		return exitUsage
	}
	_, store, err := leaseStore(*slotsPath)
	if err != nil {
		fmt.Fprintf(stderr, "lease: %v\n", err)
		return exitUsage
	}
	l, err := store.Release(server, slotID, *holder, *force)
	if err != nil {
		if r, ok := err.(*slots.Refusal); ok {
			fmt.Fprintf(stderr, "lease: %s\n", r.Msg)
			return exitSlotRefused
		}
		fmt.Fprintf(stderr, "lease: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "released %s/%s (was %s)", l.Server, l.Slot, l.Holder)
	if *force {
		fmt.Fprint(stdout, " (forced)")
	}
	fmt.Fprintln(stdout)
	return exitOK
}

// leaseListRedacted is the --json view of a SlotState without the account name.
type leaseListRedacted struct {
	Server     string        `json:"server"`
	Slot       leaseSlotView `json:"slot"`
	State      string        `json:"state"`
	Lease      *slots.Lease  `json:"lease,omitempty"`
	AgentPID   int           `json:"agent_pid,omitempty"`
	AgentAlive bool          `json:"agent_alive,omitempty"`
}

type leaseSlotView struct {
	ID              string   `json:"id"`
	Character       string   `json:"character,omitempty"`
	OtherCharacters []string `json:"other_characters,omitempty"`
	Port            int      `json:"port,omitempty"`
	SettingsFrom    string   `json:"settings_from,omitempty"`
	Reserved        string   `json:"reserved,omitempty"`
}

func leaseListCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lease list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the slot states as JSON")
	slotsPath := fs.String("slots", "", "slot table file")
	pos, rest := splitPositionals(args, 1)
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if len(pos) > 1 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, leaseUsage)
		return exitUsage
	}
	server := ""
	if len(pos) == 1 {
		server = pos[0]
	}
	_, store, err := leaseStore(*slotsPath)
	if err != nil {
		fmt.Fprintf(stderr, "lease: %v\n", err)
		return exitUsage
	}
	states, err := store.List(server)
	if err != nil {
		fmt.Fprintf(stderr, "lease: %v\n", err)
		return exitUsage
	}
	if *asJSON {
		out := make([]leaseListRedacted, 0, len(states))
		for _, s := range states {
			out = append(out, leaseListRedacted{
				Server: s.Server,
				Slot: leaseSlotView{
					ID:              s.Slot.ID,
					Character:       s.Slot.Character,
					OtherCharacters: s.Slot.OtherCharacters,
					Port:            s.Slot.Port,
					SettingsFrom:    s.Slot.SettingsFrom,
					Reserved:        s.Slot.Reserved,
				},
				State:      s.State,
				Lease:      s.Lease,
				AgentPID:   s.AgentPID,
				AgentAlive: s.AgentAlive,
			})
		}
		raw, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "lease: %v\n", err)
			return exitUsage
		}
		fmt.Fprintln(stdout, string(raw))
		return exitOK
	}
	for _, s := range states {
		char := s.Slot.Character
		if char == "" {
			char = "-"
		}
		port := "-"
		if s.Slot.Port != 0 {
			port = fmt.Sprint(s.Slot.Port)
		}
		fmt.Fprintf(stdout, "%-14s %-3s %-12s %-5s %s\n",
			s.Server, s.Slot.ID, char, port, leaseStateText(s))
	}
	return exitOK
}

func leaseStateText(s slots.SlotState) string {
	switch s.State {
	case "reserved":
		return "reserved: " + s.Slot.Reserved
	case "free":
		return "free"
	case "stale":
		detail := fmt.Sprintf("stale: held by %s, expired %s",
			s.Lease.Holder, s.Lease.Expires.UTC().Format("15:04:05Z"))
		if s.AgentAlive {
			detail += fmt.Sprintf(", agent pid %d running", s.AgentPID)
		} else {
			detail += ", no agent"
		}
		return detail + " (acquire takes it over)"
	case "held":
		detail := fmt.Sprintf("held by %s since %s, expires %s",
			s.Lease.Holder,
			s.Lease.Acquired.UTC().Format("15:04:05Z"),
			s.Lease.Expires.UTC().Format("15:04:05Z"))
		if s.AgentAlive {
			detail += fmt.Sprintf(", agent pid %d running", s.AgentPID)
		} else {
			detail += ", no agent"
		}
		if s.Lease.Dir != "" {
			detail += ", dir " + s.Lease.Dir
		}
		return detail
	default:
		return s.State
	}
}
