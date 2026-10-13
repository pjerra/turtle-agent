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
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

// Spot is a place near hostile mobs used by the live checks (map id and world coordinates).
type Spot struct {
	Map uint32  `json:"map"`
	X   float32 `json:"x"`
	Y   float32 `json:"y"`
	Z   float32 `json:"z"`
}

type Settings struct {
	Realmd    string `json:"realmd"`
	Realm     string `json:"realm,omitempty"`
	WorldAddr string `json:"world_addr,omitempty"`
	RPC       string `json:"rpc,omitempty"`
	// RPCAuth is "on" (the default) or "off". On, every RPC call must carry a bearer token:
	// RPCToken from this file, else a random one the agent writes to rpc-<port>.token next
	// to the settings file. Off is a transition switch and leaves the RPC open to every
	// local process.
	RPCAuth   string            `json:"rpc_auth,omitempty"`
	RPCToken  string            `json:"rpc_token,omitempty"`
	Accounts  map[string]string `json:"accounts"`
	Bots      []string          `json:"bots,omitempty"`
	TestSpell uint32            `json:"test_spell,omitempty"`
	Spot      *Spot             `json:"spot,omitempty"`
	// MmapsDir holds copies of the server's data/mmaps files (NNN.mmap, NNNyyxx.mmtile)
	// for path_to and walk_path. A leading ~/ is the home folder.
	MmapsDir string `json:"mmaps_dir,omitempty"`
	// TBMAddonDir is the TortoiseBotsManager checkout addon_start runs (default: the pinned copy
	// tools/tbmcontract/fetch.sh keeps under the working directory). A leading ~/ is the home folder.
	TBMAddonDir string `json:"tbm_addon_dir,omitempty"`
	// DungeonsDir holds the dungeon plans dungeon_run reads (<name>.json; default "dungeons" under the
	// working directory). A leading ~/ is the home folder.
	DungeonsDir string `json:"dungeons_dir,omitempty"`
	// Addons overrides the addon list sent at world login (default: a stock 1.12.1 client's
	// Blizzard_* set). It must include the four fingerprint addons, see worldconn.StockAddons.
	Addons []string `json:"addons,omitempty"`
	// SharedFingerprint makes every account present one addon fingerprint (one file, as for
	// one real PC). Off by default: the core's anticheat bans by a hash of fingerprint and IP,
	// so a shared print marked on one account would reach all of them.
	SharedFingerprint bool `json:"shared_fingerprint,omitempty"`
	// FingerprintDir is where the server-issued addon fingerprints are kept between runs: the
	// folder of the settings file. Set by Load, not read from JSON.
	FingerprintDir string `json:"-"`
}

const DefaultRPC = "127.0.0.1:8765"

// MinTokenLen is the shortest rpc_token accepted; the generated ones are 43 characters.
const MinTokenLen = 16

// AuthOn reports whether RPC calls need a token (everything but rpc_auth "off").
func (s Settings) AuthOn() bool { return s.RPCAuth != "off" }

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
	for _, p := range []*string{&s.MmapsDir, &s.TBMAddonDir, &s.DungeonsDir} {
		if rest, ok := strings.CutPrefix(*p, "~/"); ok {
			if home, err := os.UserHomeDir(); err == nil {
				*p = filepath.Join(home, rest)
			}
		}
	}
	if err := s.validate(); err != nil {
		return Settings{}, fmt.Errorf("settings %s: %w", path, err)
	}
	s.FingerprintDir = filepath.Dir(path)
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
	switch s.RPCAuth {
	case "", "on", "off":
	default:
		return errors.New(`"rpc_auth" must be "on" or "off"`)
	}
	if s.RPCToken != "" {
		if s.RPCAuth == "off" {
			return errors.New(`"rpc_token" is set but "rpc_auth" is "off"`)
		}
		if len(s.RPCToken) < MinTokenLen {
			return fmt.Errorf(`"rpc_token" must be at least %d characters`, MinTokenLen)
		}
		for _, c := range s.RPCToken { // it travels in a header: visible ASCII, no space
			if c <= ' ' || c > '~' {
				return errors.New(`"rpc_token" may hold visible ASCII characters only (no spaces)`)
			}
		}
	}
	if s.MmapsDir != "" {
		if info, err := os.Stat(s.MmapsDir); err != nil || !info.IsDir() {
			return fmt.Errorf(`"mmaps_dir" %s is not a folder (copy the server's data/mmaps files there)`, s.MmapsDir)
		}
	}
	if s.Addons != nil {
		if err := worldconn.ValidateAddonNames(s.Addons); err != nil {
			return fmt.Errorf(`"addons": %w`, err)
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
	return fmt.Sprintf("Settings{realmd=%s realm=%q rpc=%s rpc_auth=%s accounts=%d passwords=redacted}", s.Realmd, s.Realm, s.RPC, s.rpcAuth(), len(s.Accounts))
}

func (s Settings) rpcAuth() string {
	if s.AuthOn() {
		return "on"
	}
	return "off"
}

// GoString keeps passwords out of %#v.
func (s Settings) GoString() string { return s.String() }
