package slots

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	Version int      `json:"version"`
	Servers []Server `json:"servers"`
}

type Server struct {
	Name      string `json:"name"`
	Realmd    string `json:"realmd"`
	Realm     string `json:"realm,omitempty"`
	WorldAddr string `json:"world_addr,omitempty"`
	MmapsDir  string `json:"mmaps_dir,omitempty"`
	StateDir  string `json:"state_dir,omitempty"`
	Slots     []Slot `json:"slots"`
}

type Slot struct {
	ID              string   `json:"id"`
	Account         string   `json:"account,omitempty"`
	Character       string   `json:"character,omitempty"`
	OtherCharacters []string `json:"other_characters,omitempty"`
	Port            int      `json:"port,omitempty"`
	SettingsFrom    string   `json:"settings_from,omitempty"`
	Reserved        string   `json:"reserved,omitempty"`
}

var neverLeasable = map[string]string{
	"m910q/3":          "owner rule: never slot 3 on m910q",
	"tortoise-test2/3": "owner rule: never slot 3 on tortoise-test2",
}

var (
	serverNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	slotIDRe     = regexp.MustCompile(`^[A-Za-z0-9]{1,8}$`)
	characterRe  = regexp.MustCompile(`^[A-Za-z]{2,12}$`)
)

func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "turtle-agent", "slots.json")
}

func ExpandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

func Load(path string) (*Config, error) {
	if fi, err := os.Stat(path); err != nil {
		return nil, err
	} else if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("slots %s has mode %04o: it decides which account a client gets; run: chmod 600 %s", path, fi.Mode().Perm(), path)
	}
	dir := filepath.Dir(path)
	if di, err := os.Stat(dir); err != nil {
		return nil, err
	} else if di.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("slots %s has mode %04o: it decides which account a client gets; run: chmod 600 %s", dir, di.Mode().Perm(), path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("slots %s: %w", path, err)
	}
	if err := cfg.validate(path); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate(path string) error {
	if c.Version != 1 {
		return fmt.Errorf("slots %s: version must be 1", path)
	}
	seenServer := map[string]bool{}
	type portUse struct{ server, slot string }
	seenPort := map[int]portUse{}
	for i := range c.Servers {
		srv := &c.Servers[i]
		if !serverNameRe.MatchString(srv.Name) {
			return fmt.Errorf("slots %s: server %q: name must be lower-case letters, digits and -", path, srv.Name)
		}
		if seenServer[srv.Name] {
			return fmt.Errorf("slots %s: server %q twice", path, srv.Name)
		}
		seenServer[srv.Name] = true
		if srv.Realmd == "" {
			return fmt.Errorf("slots %s: server %q: realmd is required (host:port)", path, srv.Name)
		}
		if srv.Slots == nil {
			return fmt.Errorf("slots %s: server %q: has no slots", path, srv.Name)
		}
		seenSlot := map[string]bool{}
		for j := range srv.Slots {
			sl := &srv.Slots[j]
			if !slotIDRe.MatchString(sl.ID) {
				return fmt.Errorf("slots %s: server %s slot %q: id must be 1-8 letters or digits", path, srv.Name, sl.ID)
			}
			if seenSlot[sl.ID] {
				return fmt.Errorf("slots %s: server %s slot %q: duplicate slot id", path, srv.Name, sl.ID)
			}
			seenSlot[sl.ID] = true
			if sl.Reserved != "" {
				continue
			}
			if sl.Account == "" || sl.Character == "" || sl.Port == 0 || sl.SettingsFrom == "" {
				return fmt.Errorf("slots %s: server %s slot %q: needs account, character, port and settings_from", path, srv.Name, sl.ID)
			}
			if !characterRe.MatchString(sl.Character) {
				return fmt.Errorf("slots %s: server %s slot %q: character %q is not a character name", path, srv.Name, sl.ID, sl.Character)
			}
			if sl.Port < 1024 || sl.Port > 65535 {
				return fmt.Errorf("slots %s: server %s slot %q: port %d: 1024-65535", path, srv.Name, sl.ID, sl.Port)
			}
			if prev, ok := seenPort[sl.Port]; ok {
				return fmt.Errorf("slots %s: port %d is used by %s/%s and %s/%s (ports are per laptop: one per slot over all servers)", path, sl.Port, prev.server, prev.slot, srv.Name, sl.ID)
			}
			seenPort[sl.Port] = portUse{srv.Name, sl.ID}
		}
	}
	return nil
}

func (c *Config) findServer(server string) *Server {
	for i := range c.Servers {
		if c.Servers[i].Name == server {
			return &c.Servers[i]
		}
	}
	return nil
}

func (c *Config) Find(server, id string) (*Server, *Slot, error) {
	srv := c.findServer(server)
	if srv == nil {
		names := make([]string, 0, len(c.Servers))
		for i := range c.Servers {
			names = append(names, c.Servers[i].Name)
		}
		return nil, nil, fmt.Errorf("unknown server %q (slots.json has %s)", server, strings.Join(names, ", "))
	}
	for i := range srv.Slots {
		if srv.Slots[i].ID == id {
			sl := srv.Slots[i]
			if text, ok := neverLeasable[server+"/"+id]; ok && sl.Reserved == "" {
				sl.Reserved = text
			}
			return srv, &sl, nil
		}
	}
	if text, ok := neverLeasable[server+"/"+id]; ok {
		return srv, &Slot{ID: id, Reserved: text}, nil
	}
	ids := make([]string, 0, len(srv.Slots))
	for i := range srv.Slots {
		ids = append(ids, srv.Slots[i].ID)
	}
	return nil, nil, fmt.Errorf("unknown slot %q on %s (it has %s)", id, server, strings.Join(ids, ", "))
}
