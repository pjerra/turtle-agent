// Package config loads the local settings file. It holds account passwords, so no
// value read here may reach a log line, an error, an event or a capture.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/pjerra/turtle-agent/internal/netutil"
)

// Spot is a place near hostile mobs used by the live checks (map id and world coordinates).
type Spot struct {
	Map uint32  `json:"map"`
	X   float32 `json:"x"`
	Y   float32 `json:"y"`
	Z   float32 `json:"z"`
}

type Settings struct {
	Realmd    string            `json:"realmd"`
	Realm     string            `json:"realm,omitempty"`
	WorldAddr string            `json:"world_addr,omitempty"`
	RPC       string            `json:"rpc,omitempty"`
	Accounts  map[string]string `json:"accounts"`
	Bots      []string          `json:"bots,omitempty"`
	TestSpell uint32            `json:"test_spell,omitempty"`
	Spot      *Spot             `json:"spot,omitempty"`
	// MmapsDir holds copies of the server's data/mmaps files (NNN.mmap, NNNyyxx.mmtile)
	// for path_to and walk_path. A leading ~/ is the home folder.
	MmapsDir string `json:"mmaps_dir,omitempty"`
}

const DefaultRPC = "127.0.0.1:8765"

func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "turtle-agent", "settings.json")
	}
	return filepath.Join(home, ".config", "turtle-agent", "settings.json")
}

func Load(path string) (Settings, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Settings{}, fmt.Errorf("settings %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return Settings{}, fmt.Errorf("settings %s has mode %04o; it holds passwords, run: chmod 600 %s", path, perm, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, fmt.Errorf("settings %s: %w", path, err)
	}
	var s Settings
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		detail := ""
		if msg := err.Error(); strings.HasPrefix(msg, "json: unknown field ") {
			detail = ": " + strings.TrimPrefix(msg, "json: ") // field names are not secret
		}
		return Settings{}, fmt.Errorf("settings %s is not valid settings JSON%s", path, detail)
	}
	if s.RPC == "" {
		s.RPC = DefaultRPC
	}
	if rest, ok := strings.CutPrefix(s.MmapsDir, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			s.MmapsDir = filepath.Join(home, rest)
		}
	}
	if err := s.validate(); err != nil {
		return Settings{}, fmt.Errorf("settings %s: %w", path, err)
	}
	return s, nil
}

func (s Settings) validate() error {
	if host, port, err := net.SplitHostPort(s.Realmd); err != nil || host == "" || port == "" {
		return errors.New(`"realmd" must be host:port (RealmServerPort from the server's realmd.conf)`)
	}
	if s.WorldAddr != "" {
		if _, _, err := net.SplitHostPort(s.WorldAddr); err != nil {
			return errors.New(`"world_addr" must be host:port`)
		}
	}
	if _, _, err := netutil.CheckLoopbackAddr(s.RPC); err != nil {
		return fmt.Errorf(`"rpc" %w`, err)
	}
	if s.MmapsDir != "" {
		if info, err := os.Stat(s.MmapsDir); err != nil || !info.IsDir() {
			return fmt.Errorf(`"mmaps_dir" %s is not a folder (copy the server's data/mmaps files there)`, s.MmapsDir)
		}
	}
	if len(s.Accounts) == 0 {
		return errors.New(`"accounts" must map at least one account name to its password`)
	}
	for name, pw := range s.Accounts {
		if name == "" || pw == "" {
			return errors.New(`"accounts" has an empty account name or password`)
		}
	}
	return nil
}

// Password looks the account up case-insensitively (realmd upper-cases account names).
// The error does not name the account (ruling P10: account names stay out of logs).
func (s Settings) Password(account string) (string, error) {
	for name, pw := range s.Accounts {
		if strings.EqualFold(name, account) {
			return pw, nil
		}
	}
	return "", errors.New("the requested account is not in the settings file")
}

// OnlyAccount is the account name when the file holds exactly one, so `login <character>`
// keeps the name off the command line (ps, shell history). The error lists no names.
func (s Settings) OnlyAccount() (string, error) {
	if len(s.Accounts) != 1 {
		return "", fmt.Errorf("the settings file holds %d accounts; name one: login <account> <character>", len(s.Accounts))
	}
	for name := range s.Accounts {
		return name, nil
	}
	return "", nil
}

// String keeps passwords and account names out of %v, %+v and %s formatting.
func (s Settings) String() string {
	return fmt.Sprintf("Settings{realmd=%s realm=%q rpc=%s accounts=%d passwords=redacted}", s.Realmd, s.Realm, s.RPC, len(s.Accounts))
}

// GoString keeps passwords out of %#v.
func (s Settings) GoString() string { return s.String() }
