package slots

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// newLeaseID mints an unguessable 128-bit lease identity (32 hex chars).
// Every acquisition gets one; release/renew/SetDir must present the exact
// ID, so a holder string alone is never enough (Codex ABA).
func newLeaseID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

type Lease struct {
	Server        string    `json:"server"`
	Slot          string    `json:"slot"`
	Holder        string    `json:"holder"`        // ^[A-Za-z0-9._-]{1,64}$
	LeaseID       string    `json:"lease_id"`      // unguessable per-acquisition identity
	Acquired      time.Time `json:"acquired"`      // UTC
	Expires       time.Time `json:"expires"`       // UTC
	Dir           string    `json:"dir,omitempty"` // set by slot setup: the client dir (absolute)
	TakenOverFrom string    `json:"taken_over_from,omitempty"`
	// The holder's process, recorded when a long-lived holder (a walk) takes the lease: another reader on the same
	// host can then prove the holder is gone (reap) instead of waiting out the TTL. An old-format lease has none and
	// keeps the expiry rule.
	HolderPID   int       `json:"holder_pid,omitempty"`
	HolderHost  string    `json:"holder_host,omitempty"`
	HolderStart uint64    `json:"holder_start,omitempty"` // /proc/<pid>/stat start time: tells a reused pid from the holder
	Renewed     time.Time `json:"renewed,omitzero"`       // UTC, the last acquire/renew
}

// leaseHost is this machine's name, as leases record it.
var leaseHost = func() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// holderProc reads a pid's start time and whether it is alive; a test replaces it. exact is false when the
// platform cannot tell, and an unknown holder is never called dead.
var holderProc = procStart

type holderLiveness int

const (
	holderUnknown holderLiveness = iota // not recorded, another host, or the platform cannot tell
	holderAlive
	holderDead
)

// holderState says whether the lease's recorded holder process still holds it here. A lease of another host, an
// old-format lease without the fields and one this platform cannot check are unknown: only the expiry frees them.
// A local pid that is gone, or alive with a different start time (the pid was reused), is dead.
func holderState(l *Lease) holderLiveness {
	if l == nil || l.HolderPID <= 0 || l.HolderHost == "" || l.HolderHost != leaseHost() {
		return holderUnknown
	}
	start, alive, exact := holderProc(l.HolderPID)
	if !exact {
		return holderUnknown
	}
	if !alive {
		return holderDead
	}
	if l.HolderStart > 0 && l.HolderStart != start {
		return holderDead
	}
	return holderAlive
}

type Probe interface {
	AgentPID(dir string) (pid int, alive bool)
	PortListening(port int) bool
}

type Store struct {
	Cfg   *Config
	Dir   string
	Now   func() time.Time
	Probe Probe
	// HolderPID, when > 0, is recorded in every lease this store acquires or renews as the holder's process (a walk
	// sets its own pid). A CLI acquire leaves it 0: that process exits at once, so only the expiry frees its lease.
	HolderPID int
}

func NewStore(cfg *Config, slotsPath string) *Store {
	return &Store{
		Cfg:   cfg,
		Dir:   filepath.Join(filepath.Dir(slotsPath), "leases"),
		Now:   time.Now,
		Probe: LinuxProbe{},
	}
}

// LeasesDir is where the store of slotsPath keeps its leases: beside the slot table, one file per server and slot.
func LeasesDir(slotsPath string) string {
	return filepath.Join(filepath.Dir(slotsPath), "leases")
}

// LiveLeaseHolders names the holders of server's live leases in dir (a store's leases dir). A lease is live unless the
// store would call it stale (expired AND no live agent in its dir) or its recorded local holder process is dead. A
// missing dir is no leases; an unreadable file is an error, because an unknown ledger is not called empty.
func LiveLeaseHolders(dir, server string, now time.Time, probe Probe) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, server))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, server, e.Name()))
		if err != nil {
			return nil, err
		}
		var l Lease
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, fmt.Errorf("lease %s: %w", e.Name(), err)
		}
		live := !now.After(l.Expires)
		if holderState(&l) == holderDead {
			live = false
		} else if !live && l.Dir != "" && probe != nil {
			if _, alive := probe.AgentPID(l.Dir); alive {
				live = true
			}
		}
		if live {
			out = append(out, l.Holder)
		}
	}
	sort.Strings(out)
	return out, nil
}

type Acquired struct {
	Lease    Lease
	Slot     Slot
	TookOver *Lease
	Renewed  bool
}

type SlotState struct {
	Server     string
	Slot       Slot
	State      string
	Lease      *Lease
	AgentPID   int
	AgentAlive bool
	// HolderDead says the lease's recorded local holder process is gone (its pid is dead or reused): the slot is
	// free at once, whatever the expiry says.
	HolderDead bool
}

type Refusal struct {
	Msg string
}

func (r *Refusal) Error() string { return r.Msg }

func fmtClock(t time.Time) string {
	return t.UTC().Format("15:04:05Z")
}

func (s *Store) leasePath(server, slot string) string {
	return filepath.Join(s.Dir, server, slot+".json")
}

func (s *Store) readLease(server, slot string) (*Lease, error) {
	raw, err := os.ReadFile(s.leasePath(server, slot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var l Lease
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *Store) writeLease(l *Lease) error {
	dir := filepath.Dir(s.leasePath(l.Server, l.Slot))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := s.leasePath(l.Server, l.Slot) + fmt.Sprintf(".tmp.%d", os.Getpid())
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.leasePath(l.Server, l.Slot))
}

func (s *Store) agentAlive(dir string) (int, bool) {
	if dir == "" {
		return 0, false
	}
	return s.Probe.AgentPID(dir)
}

// stampHolder writes the acquire/renew side of a lease: the renewal clock always, and this store's holder process
// (pid, host, start time) when one was recorded. A walk's lease can then be freed at once when the walk dies; a
// CLI lease (HolderPID 0) keeps the expiry rule.
func (s *Store) stampHolder(l *Lease, now time.Time) {
	l.Renewed = now.UTC()
	if s.HolderPID <= 0 {
		return
	}
	l.HolderPID = s.HolderPID
	l.HolderHost = leaseHost()
	l.HolderStart = 0
	if start, alive, exact := holderProc(s.HolderPID); exact && alive {
		l.HolderStart = start
	}
}

func (s *Store) isStale(l *Lease) bool {
	if l == nil {
		return false
	}
	if holderState(l) == holderDead {
		return true
	}
	if !s.Now().After(l.Expires) {
		return false
	}
	_, alive := s.agentAlive(l.Dir)
	return !alive
}

// IsStale reports whether a lease read by Get needs a takeover: a dead local holder, or an expiry with no live agent.
func (s *Store) IsStale(l *Lease) bool { return s.isStale(l) }

func (s *Store) withLock(fn func() error) error {
	unlock, err := lockDir(s.Dir)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func (s *Store) Acquire(server, slot string, any bool, holder string, ttl time.Duration) (Acquired, error) {
	var out Acquired
	var acquireErr error
	if withErr := s.withLock(func() error {
		var err error
		out, err = s.acquireLocked(server, slot, any, holder, ttl)
		return err
	}); withErr != nil {
		acquireErr = withErr
	}
	return out, acquireErr
}

func (s *Store) acquireLocked(server, slot string, any bool, holder string, ttl time.Duration) (Acquired, error) {
	srv := s.Cfg.findServer(server)
	if srv == nil {
		_, _, err := s.Cfg.Find(server, "")
		return Acquired{}, err
	}
	if any {
		for i := range srv.Slots {
			sl := srv.Slots[i]
			if text, ok := neverLeasable[server+"/"+sl.ID]; ok && sl.Reserved == "" {
				sl.Reserved = text
			}
			if sl.Reserved != "" {
				continue
			}
			existing, err := s.readLease(server, sl.ID)
			if err != nil {
				return Acquired{}, err
			}
			if existing != nil && existing.Holder == holder {
				continue
			}
			if existing != nil && !s.isStale(existing) {
				continue
			}
			if s.Probe.PortListening(sl.Port) {
				continue
			}
			now := s.Now().UTC()
			id, err := newLeaseID()
			if err != nil {
				return Acquired{}, err
			}
			l := &Lease{Server: server, Slot: sl.ID, Holder: holder, LeaseID: id, Acquired: now, Expires: now.Add(ttl)}
			s.stampHolder(l, now)
			if existing != nil {
				old := *existing
				out := Acquired{Lease: *l, Slot: sl, TookOver: &old}
				l.TakenOverFrom = existing.Holder
				out.Lease = *l
				if err := s.writeLease(l); err != nil {
					return Acquired{}, err
				}
				return out, nil
			}
			if err := s.writeLease(l); err != nil {
				return Acquired{}, err
			}
			return Acquired{Lease: *l, Slot: sl}, nil
		}
		parts := make([]string, 0, len(srv.Slots))
		for i := range srv.Slots {
			sl := srv.Slots[i]
			if text, ok := neverLeasable[server+"/"+sl.ID]; ok && sl.Reserved == "" {
				sl.Reserved = text
			}
			switch {
			case sl.Reserved != "":
				parts = append(parts, sl.ID+" reserved")
			default:
				existing, err := s.readLease(server, sl.ID)
				if err != nil {
					return Acquired{}, err
				}
				switch {
				case existing != nil && !s.isStale(existing):
					parts = append(parts, sl.ID+" held by "+existing.Holder)
				case s.Probe.PortListening(sl.Port):
					parts = append(parts, fmt.Sprintf("%s port %d busy", sl.ID, sl.Port))
				case existing != nil:
					parts = append(parts, sl.ID+" held by "+existing.Holder)
				default:
					parts = append(parts, sl.ID+" free")
				}
			}
		}
		msgs := "no free slot on " + server
		if len(parts) > 0 {
			msgs += ": " + joinComma(parts)
		}
		return Acquired{}, &Refusal{Msg: msgs}
	}
	_, sl, err := s.Cfg.Find(server, slot)
	if err != nil {
		return Acquired{}, err
	}
	if sl.Reserved != "" {
		return Acquired{}, &Refusal{Msg: fmt.Sprintf("%s/%s is reserved (%s)", server, slot, sl.Reserved)}
	}
	existing, err := s.readLease(server, slot)
	if err != nil {
		return Acquired{}, err
	}
	now := s.Now().UTC()
	if existing != nil && existing.Holder == holder {
		id, err := newLeaseID()
		if err != nil {
			return Acquired{}, err
		}
		existing.LeaseID = id
		existing.Expires = now.Add(ttl)
		s.stampHolder(existing, now)
		if err := s.writeLease(existing); err != nil {
			return Acquired{}, err
		}
		return Acquired{Lease: *existing, Slot: *sl, Renewed: true}, nil
	}
	if existing != nil && !s.isStale(existing) {
		pid, alive := s.agentAlive(existing.Dir)
		var tail string
		if alive {
			tail = fmt.Sprintf("(agent pid %d running): pick another slot or wait", pid)
		} else {
			tail = "(no agent running; it can be taken over after the expiry)"
		}
		return Acquired{}, &Refusal{Msg: fmt.Sprintf("%s/%s is held by %s since %s until %s %s",
			server, slot, existing.Holder, fmtClock(existing.Acquired), fmtClock(existing.Expires), tail)}
	}
	var took *Lease
	id, err := newLeaseID()
	if err != nil {
		return Acquired{}, err
	}
	l := &Lease{Server: server, Slot: slot, Holder: holder, LeaseID: id, Acquired: now, Expires: now.Add(ttl)}
	s.stampHolder(l, now)
	if existing != nil {
		old := *existing
		took = &old
		l.TakenOverFrom = existing.Holder
	}
	if s.Probe.PortListening(sl.Port) {
		return Acquired{}, &Refusal{Msg: fmt.Sprintf("%s/%s: port %d has a listener on 127.0.0.1 (an agent outside the leases?): stop it or pick another slot",
			server, slot, sl.Port)}
	}
	if err := s.writeLease(l); err != nil {
		return Acquired{}, err
	}
	return Acquired{Lease: *l, Slot: *sl, TookOver: took}, nil
}

func (s *Store) Renew(server, slot, holder, leaseID string, ttl time.Duration) (Lease, error) {
	var out Lease
	err := s.withLock(func() error {
		if _, _, err := s.Cfg.Find(server, slot); err != nil {
			return err
		}
		existing, err := s.readLease(server, slot)
		if err != nil {
			return err
		}
		if existing == nil {
			return &Refusal{Msg: fmt.Sprintf("%s/%s is not leased by %s (free)", server, slot, holder)}
		}
		if existing.Holder != holder {
			return &Refusal{Msg: fmt.Sprintf("%s/%s is not leased by %s (held by %s)", server, slot, holder, existing.Holder)}
		}
		if existing.LeaseID != leaseID {
			return &Refusal{Msg: fmt.Sprintf("%s/%s is not leased by %s (lease id mismatch: it was re-acquired)", server, slot, holder)}
		}
		now := s.Now().UTC()
		existing.Expires = now.Add(ttl)
		s.stampHolder(existing, now)
		if err := s.writeLease(existing); err != nil {
			return err
		}
		out = *existing
		return nil
	})
	return out, err
}

func (s *Store) Release(server, slot, holder, leaseID string, force bool) (Lease, error) {
	var out Lease
	err := s.withLock(func() error {
		if _, _, err := s.Cfg.Find(server, slot); err != nil {
			return err
		}
		existing, err := s.readLease(server, slot)
		if err != nil {
			return err
		}
		if existing == nil {
			return &Refusal{Msg: fmt.Sprintf("%s/%s is not leased", server, slot)}
		}
		if !force {
			if existing.Holder != holder {
				return &Refusal{Msg: fmt.Sprintf("%s/%s is held by %s, not %s: only the holder releases it (the lead may pass --force)",
					server, slot, existing.Holder, holder)}
			}
			if existing.LeaseID != leaseID {
				return &Refusal{Msg: fmt.Sprintf("%s/%s is not leased by %s (lease id mismatch: it was re-acquired; the lead may pass --force)",
					server, slot, holder)}
			}
			if pid, alive := s.agentAlive(existing.Dir); alive {
				return &Refusal{Msg: fmt.Sprintf("%s/%s: its agent (pid %d) is still running: run turtle-agent teardown <dir> first",
					server, slot, pid)}
			}
		}
		out = *existing
		if err := os.Remove(s.leasePath(server, slot)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
	return out, err
}

func (s *Store) List(server string) ([]SlotState, error) {
	var out []SlotState
	err := s.withLock(func() error {
		servers := s.Cfg.Servers
		if server != "" {
			srv := s.Cfg.findServer(server)
			if srv == nil {
				_, _, err := s.Cfg.Find(server, "")
				return err
			}
			servers = []Server{*srv}
		}
		for si := range servers {
			srv := &servers[si]
			for i := range srv.Slots {
				sl := srv.Slots[i]
				if text, ok := neverLeasable[srv.Name+"/"+sl.ID]; ok && sl.Reserved == "" {
					sl.Reserved = text
				}
				st := SlotState{Server: srv.Name, Slot: sl}
				if sl.Reserved != "" {
					st.State = "reserved"
					out = append(out, st)
					continue
				}
				existing, err := s.readLease(srv.Name, sl.ID)
				if err != nil {
					return err
				}
				if existing == nil {
					st.State = "free"
					out = append(out, st)
					continue
				}
				l := *existing
				st.Lease = &l
				st.HolderDead = holderState(existing) == holderDead
				if pid, alive := s.agentAlive(existing.Dir); alive || pid != 0 {
					st.AgentPID = pid
					st.AgentAlive = alive
				}
				if s.isStale(existing) {
					st.State = "stale"
				} else {
					st.State = "held"
				}
				out = append(out, st)
			}
		}
		return nil
	})
	return out, err
}

func (s *Store) SetDir(server, slot, holder, leaseID, dir string) error {
	return s.withLock(func() error {
		if _, _, err := s.Cfg.Find(server, slot); err != nil {
			return err
		}
		existing, err := s.readLease(server, slot)
		if err != nil {
			return err
		}
		if existing == nil {
			return &Refusal{Msg: fmt.Sprintf("%s/%s is not leased by %s (free)", server, slot, holder)}
		}
		if existing.Holder != holder {
			return &Refusal{Msg: fmt.Sprintf("%s/%s is not leased by %s (held by %s)", server, slot, holder, existing.Holder)}
		}
		if existing.LeaseID != leaseID {
			return &Refusal{Msg: fmt.Sprintf("%s/%s is not leased by %s (lease id mismatch: it was re-acquired)", server, slot, holder)}
		}
		if s.isStale(existing) {
			return &Refusal{Msg: fmt.Sprintf("%s/%s is not leased by %s (stale)", server, slot, holder)}
		}
		existing.Dir = dir
		return s.writeLease(existing)
	})
}

// Get returns a copy of the current lease, or nil when the slot is free.
// Setup uses it to re-validate the lease identity right before writing.
func (s *Store) Get(server, slot string) (*Lease, error) {
	var out *Lease
	err := s.withLock(func() error {
		if _, _, err := s.Cfg.Find(server, slot); err != nil {
			return err
		}
		existing, err := s.readLease(server, slot)
		if err != nil {
			return err
		}
		if existing != nil {
			l := *existing
			out = &l
		}
		return nil
	})
	return out, err
}

// ReapedLease is one lease a reap removed, or with dryRun would remove.
type ReapedLease struct {
	Server    string
	Slot      string
	Holder    string
	HolderPID int
	Expires   time.Time
	Path      string
}

// ReapDeadLeases removes the leases in dir (a store's leases dir, every server under it) whose recorded holder ran
// on this host and is gone: its pid is dead or was reused, so no renewal can come. A lease of another host, an
// old-format lease without the holder fields, and one this platform cannot check are left to their expiry. dryRun
// lists them and removes nothing. A missing dir is no leases. The store's lock keeps the reap from racing an acquire
// or a renewal, and the whole ledger is read before anything is removed, so an unreadable lease changes nothing.
func ReapDeadLeases(dir string, dryRun bool) ([]ReapedLease, error) {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	servers, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []ReapedLease
	for _, srv := range servers {
		if !srv.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(dir, srv.Name()))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, srv.Name(), e.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var l Lease
			if err := json.Unmarshal(raw, &l); err != nil {
				return nil, fmt.Errorf("lease %s: %w", path, err)
			}
			if holderState(&l) != holderDead {
				continue
			}
			out = append(out, ReapedLease{Server: l.Server, Slot: l.Slot, Holder: l.Holder, HolderPID: l.HolderPID, Expires: l.Expires, Path: path})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		return out[i].Slot < out[j].Slot
	})
	if dryRun {
		return out, nil
	}
	for _, l := range out {
		if err := os.Remove(l.Path); err != nil && !os.IsNotExist(err) {
			return out, err
		}
	}
	return out, nil
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
