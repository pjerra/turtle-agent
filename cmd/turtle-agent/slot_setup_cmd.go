// Command slot setup builds a private client dir for a leased slot.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pjerra/turtle-agent/internal/config"
	"github.com/pjerra/turtle-agent/internal/slots"
)

const slotSetupUsage = "usage: turtle-agent slot setup <server> <slot> --holder NAME [--dir DIR] [--binary PATH] [--dungeons-dir DIR] [--character NAME] [--slots FILE]"

// slotManifest is the no-secret <dir>/slot.json written by slot setup.
type slotManifest struct {
	Server    string `json:"server"`
	Slot      string `json:"slot"`
	Holder    string `json:"holder"`
	Account   string `json:"account"`
	Character string `json:"character"`
	Port      int    `json:"port"`
	Dir       string `json:"dir"`
	Binary    string `json:"binary"`
	Settings  string `json:"settings"`
	PIDFile   string `json:"pid_file"`
	TokenFile string `json:"token_file"`
	Created   string `json:"created"`
}

func slotSetupCmd(args []string, stdout, stderr io.Writer) int {
	if !slotsSupported() {
		return slotsUnsupported("slot", stderr)
	}
	fs := flag.NewFlagSet("slot setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	holder := fs.String("holder", "", "lease holder name")
	dirFlag := fs.String("dir", "", "client dir (default ~/ta-<holder>)")
	binaryFlag := fs.String("binary", "", "turtle-agent binary to install (default os.Executable())")
	dungeonsFlag := fs.String("dungeons-dir", "", "dungeons dir (default <dir>/dungeons)")
	characterFlag := fs.String("character", "", "character to play (default the slot's)")
	slotsPath := fs.String("slots", "", "slot table file")
	pos, rest := splitPositionals(args, 2)
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if len(pos) != 2 || fs.NArg() != 0 || *holder == "" {
		fmt.Fprintln(stderr, slotSetupUsage)
		return exitUsage
	}
	serverName, slotID := pos[0], pos[1]
	if !leaseHolderRe.MatchString(*holder) {
		fmt.Fprintln(stderr, "lease: --holder must be 1-64 letters, digits, . _ -")
		return exitUsage
	}
	if *slotsPath == "" {
		*slotsPath = slots.DefaultPath()
	}
	cfg, err := slots.Load(*slotsPath)
	if err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	server, slot, err := cfg.Find(serverName, slotID)
	if err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	if slot.Reserved != "" {
		fmt.Fprintf(stderr, "slot setup: %s/%s is reserved (%s)\n", server.Name, slot.ID, slot.Reserved)
		return exitSlotRefused
	}
	store := slots.NewStore(cfg, *slotsPath)
	if err := slotSetupCheckLease(store, server.Name, slot.ID, *holder); err != nil {
		fmt.Fprintf(stderr, "slot setup: %s/%s is not leased by %s: run turtle-agent lease acquire %s --slot %s --holder %s first\n",
			server.Name, slot.ID, *holder, server.Name, slot.ID, *holder)
		return exitSlotRefused
	}
	character := slot.Character
	if *characterFlag != "" {
		character = *characterFlag
	}
	if !slotSetupAllows(slot, character) {
		fmt.Fprintf(stderr, "slot setup: %s/%s allows %s\n", server.Name, slot.ID, slotSetupAllowed(slot))
		return exitUsage
	}
	dir := *dirFlag
	if dir == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			fmt.Fprintf(stderr, "slot setup: %v\n", herr)
			return exitUsage
		}
		dir = filepath.Join(home, "ta-"+*holder)
	}
	dir = slots.ExpandTilde(dir)
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	dir = absDir
	if info, err := os.Stat(dir); err == nil {
		if !info.IsDir() {
			fmt.Fprintf(stderr, "slot setup: %s exists and is not empty: pick a new --dir (setup never overwrites)\n", dir)
			return exitUsage
		}
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			fmt.Fprintf(stderr, "slot setup: %v\n", rerr)
			return exitUsage
		}
		if len(entries) > 0 {
			fmt.Fprintf(stderr, "slot setup: %s exists and is not empty: pick a new --dir (setup never overwrites)\n", dir)
			return exitUsage
		}
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	} else if merr := os.MkdirAll(dir, 0o700); merr != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", merr)
		return exitUsage
	}
	binSrc := *binaryFlag
	if binSrc == "" {
		binSrc, err = os.Executable()
		if err != nil {
			fmt.Fprintf(stderr, "slot setup: %v\n", err)
			return exitUsage
		}
	}
	binDst := filepath.Join(dir, "turtle-agent")
	if err := copyFileExcl(binSrc, binDst, 0o755); err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	settingsFrom := slots.ExpandTilde(slot.SettingsFrom)
	src, err := config.Load(settingsFrom)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitUsage
	}
	pw, err := src.Password(slot.Account)
	if err != nil {
		fmt.Fprintf(stderr, "slot setup: %s does not hold the slot's account\n", slot.SettingsFrom)
		return exitUsage
	}
	dungeonsDir := *dungeonsFlag
	if dungeonsDir == "" {
		dungeonsDir = filepath.Join(dir, "dungeons")
	}
	if err := os.MkdirAll(dungeonsDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	tracesDir := filepath.Join(dir, "traces")
	if err := os.MkdirAll(tracesDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	stateDir := slots.ExpandTilde(server.StateDir)
	if stateDir == "" {
		stateDir = filepath.Dir(settingsFrom)
	} else if err := os.MkdirAll(stateDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	mmapsDir := slots.ExpandTilde(server.MmapsDir)
	if mmapsDir == "" {
		mmapsDir = src.MmapsDir
	}
	dst := config.Settings{
		Realmd:            server.Realmd,
		Realm:             server.Realm,
		WorldAddr:         server.WorldAddr,
		RPC:               fmt.Sprintf("127.0.0.1:%d", slot.Port),
		Accounts:          map[string]string{slot.Account: pw},
		MmapsDir:          mmapsDir,
		DungeonsDir:       dungeonsDir,
		TracesDir:         tracesDir,
		TestSpell:         src.TestSpell,
		Spot:              src.Spot,
		Addons:            src.Addons,
		SharedFingerprint: src.SharedFingerprint,
		RotationsDir:      src.RotationsDir,
		TBMAddonDir:       src.TBMAddonDir,
		RunMeta:           src.RunMeta,
		StateDir:          stateDir,
	}
	settingsPath := filepath.Join(dir, "settings.json")
	if err := slotSetupWriteSettings(settingsPath, dst, stateDir); err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	tokenFile := filepath.Join(dir, fmt.Sprintf("rpc-%d.token", slot.Port))
	manifest := slotManifest{
		Server:    server.Name,
		Slot:      slot.ID,
		Holder:    *holder,
		Account:   slot.Account,
		Character: character,
		Port:      slot.Port,
		Dir:       dir,
		Binary:    binDst,
		Settings:  settingsPath,
		PIDFile:   filepath.Join(dir, "agent.pid"),
		TokenFile: tokenFile,
		Created:   store.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	if err := writeFileExcl(filepath.Join(dir, "slot.json"), rawManifest, 0o600); err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	if err := store.SetDir(server.Name, slot.ID, *holder, dir); err != nil {
		if r, ok := err.(*slots.Refusal); ok {
			fmt.Fprintf(stderr, "slot setup: %s\n", r.Msg)
			return exitSlotRefused
		}
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "slot %s/%s ready in %s\n", server.Name, slot.ID, dir)
	fmt.Fprintf(stdout, "account %s, character %s, port %d\n", slot.Account, character, slot.Port)
	fmt.Fprintf(stdout, "start: nohup %s login --config %s --pid-file %s %s > %s 2>&1 &\n",
		binDst, settingsPath, manifest.PIDFile, character, filepath.Join(dir, "agent.out"))
	fmt.Fprintf(stdout, "rpc: 127.0.0.1:%d, token file %s (written at login)\n", slot.Port, tokenFile)
	fmt.Fprintf(stdout, "teardown: %s teardown %s\n", binDst, dir)
	return exitOK
}

func slotSetupCheckLease(store *slots.Store, server, slot, holder string) error {
	states, err := store.List(server)
	if err != nil {
		return err
	}
	for _, s := range states {
		if s.Slot.ID != slot {
			continue
		}
		if s.State == "held" && s.Lease != nil && s.Lease.Holder == holder {
			return nil
		}
		return fmt.Errorf("not leased by %s", holder)
	}
	return fmt.Errorf("not leased by %s", holder)
}

func slotSetupAllows(slot *slots.Slot, character string) bool {
	if character == slot.Character {
		return true
	}
	for _, other := range slot.OtherCharacters {
		if character == other {
			return true
		}
	}
	return false
}

func slotSetupAllowed(slot *slots.Slot) string {
	if len(slot.OtherCharacters) == 0 {
		return slot.Character
	}
	return slot.Character + " (and " + strings.Join(slot.OtherCharacters, ", ") + ")"
}

// slotSetupWriteSettings writes the client settings (O_EXCL, 0600), then loads
// them back; a load failure removes the file.
func slotSetupWriteSettings(path string, dst config.Settings, stateDir string) error {
	raw, err := json.MarshalIndent(dst, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileExcl(path, raw, 0o600); err != nil {
		return err
	}
	if _, err := config.Load(path); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func copyFileExcl(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		os.Remove(dst)
		return closeErr
	}
	if err := os.Chmod(dst, mode); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

func writeFileExcl(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		os.Remove(path)
		return werr
	}
	if cerr != nil {
		os.Remove(path)
		return cerr
	}
	return nil
}
