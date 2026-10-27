// Command slot setup builds a private client dir for a leased slot.
package main

import (
	"encoding/json"
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
	LeaseID   string `json:"lease_id"`
	Account   string `json:"account"`
	Character string `json:"character"`
	Port      int    `json:"port"`
	Dir       string `json:"dir"`
	Binary    string `json:"binary"`
	Settings  string `json:"settings"`
	PIDFile   string `json:"pid_file"`
	TokenFile string `json:"token_file"`
	SlotsPath string `json:"slots_path"`
	Created   string `json:"created"`
}

// slotSetupPreWriteHook runs right before setup re-validates the lease and
// writes settings.json. Tests set it to simulate a takeover between the
// initial lease check and the write; it is nil in production.
var slotSetupPreWriteHook func()

func slotSetupCmd(args []string, stdout, stderr io.Writer) int {
	if !slotsSupported() {
		return slotsUnsupported("slot", stderr)
	}
	o, code := parseSlotSetupArgs(args, stderr)
	if code != exitOK {
		return code
	}
	// Aliases so the body below reads as before.
	holder, dirFlag := &o.holder, &o.dir
	binaryFlag, dungeonsFlag := &o.binary, &o.dungeonsDir
	characterFlag, slotsPath := &o.character, &o.slotsPath
	serverName, slotID := o.server, o.slot
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
	leaseID, err := slotSetupCheckLease(store, server.Name, slot.ID, *holder)
	if err != nil {
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
	// Claim the dir first, before any credential exists: refuse a symlink
	// --dir (or a symlink in any existing parent component), create it
	// 0700, refuse a non-empty dir, chmod an empty one to 0700. Everything
	// setup creates below is tracked for cleanup on failure; cleanup only
	// removes setup-created paths inside the claimed dir.
	if err := slotSetupCheckNoSymlink(dir); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitUsage
	}
	var created []string
	var claimInfo os.FileInfo
	cleanup := func() {
		if claimInfo != nil {
			// The dir was swapped under setup: never reach through it.
			if cur, err := os.Lstat(dir); err != nil || cur.Mode()&os.ModeSymlink != 0 || !os.SameFile(claimInfo, cur) {
				return
			}
		}
		for _, p := range created {
			if !slotSetupInsideDir(dir, p) {
				continue
			}
			os.RemoveAll(p)
		}
	}
	fail := func(format string, a ...any) int {
		cleanup()
		fmt.Fprintf(stderr, format+"\n", a...)
		return exitUsage
	}
	if info, err := os.Lstat(dir); err == nil {
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
		if err := os.Chmod(dir, 0o700); err != nil {
			fmt.Fprintf(stderr, "slot setup: %v\n", err)
			return exitUsage
		}
		claimInfo = info
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	} else if merr := os.MkdirAll(dir, 0o700); merr != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", merr)
		return exitUsage
	} else if merr := os.Chmod(dir, 0o700); merr != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", merr)
		return exitUsage
	} else if info, serr := os.Lstat(dir); serr != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", serr)
		return exitUsage
	} else {
		claimInfo = info
	}
	// Resolve the claimed dir to its real path once (a refused-symlink
	// check ran above, so this succeeds only on a plain dir): children
	// are built from the real path, never through a later-swapped link.
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	dir = realDir
	binSrc := *binaryFlag
	if binSrc == "" {
		binSrc, err = os.Executable()
		if err != nil {
			return fail("slot setup: %v", err)
		}
	}
	binDst := filepath.Join(dir, "turtle-agent")
	if err := copyFileExcl(binSrc, binDst, 0o755); err != nil {
		return fail("slot setup: %v", err)
	}
	created = append(created, binDst)
	settingsFrom := slots.ExpandTilde(slot.SettingsFrom)
	src, err := config.Load(settingsFrom)
	if err != nil {
		return fail("%v", err)
	}
	pw, err := src.Password(slot.Account)
	if err != nil {
		return fail("slot setup: %s does not hold the slot's account", slot.SettingsFrom)
	}
	dungeonsDir := *dungeonsFlag
	if dungeonsDir == "" {
		dungeonsDir = filepath.Join(dir, "dungeons")
	}
	// Only a dir setup created itself is tracked for cleanup: a
	// pre-existing caller-supplied --dungeons-dir is never removed.
	dungeonsMade, err := slotSetupEnsureDir(dungeonsDir, 0o755)
	if err != nil {
		return fail("slot setup: %v", err)
	}
	if dungeonsMade {
		created = append(created, dungeonsDir)
	}
	tracesDir := filepath.Join(dir, "traces")
	tracesMade, err := slotSetupEnsureDir(tracesDir, 0o755)
	if err != nil {
		return fail("slot setup: %v", err)
	}
	if tracesMade {
		created = append(created, tracesDir)
	}
	stateDir := slots.ExpandTilde(server.StateDir)
	if stateDir == "" {
		stateDir = filepath.Dir(settingsFrom)
	} else if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fail("slot setup: %v", err)
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
	if slotSetupPreWriteHook != nil {
		slotSetupPreWriteHook()
	}
	// Re-validate the lease identity immediately before writing settings:
	// a takeover between the check and the write must leave no credentials.
	cur, err := store.Get(server.Name, slot.ID)
	if err != nil {
		return fail("slot setup: %v", err)
	}
	if cur == nil || cur.Holder != *holder || cur.LeaseID != leaseID || store.IsStale(cur) {
		cleanup()
		fmt.Fprintf(stderr, "slot setup: %s/%s is not leased by %s: run turtle-agent lease acquire %s --slot %s --holder %s first\n",
			server.Name, slot.ID, *holder, server.Name, slot.ID, *holder)
		return exitSlotRefused
	}
	// The dir must still be the claimed one (not a swapped-in symlink)
	// right before settings.json lands: re-check via Lstat.
	curInfo, err := os.Lstat(dir)
	if err != nil || curInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(claimInfo, curInfo) {
		return fail("slot setup: %s was replaced during setup: refusing to write settings", dir)
	}

	settingsPath := filepath.Join(dir, "settings.json")
	if err := slotSetupWriteSettings(settingsPath, dst, stateDir); err != nil {
		return fail("slot setup: %v", err)
	}
	created = append(created, settingsPath)
	tokenFile := filepath.Join(dir, fmt.Sprintf("rpc-%d.token", slot.Port))
	manifest := slotManifest{
		Server:    server.Name,
		Slot:      slot.ID,
		Holder:    *holder,
		LeaseID:   leaseID,
		Account:   slot.Account,
		Character: character,
		Port:      slot.Port,
		Dir:       dir,
		Binary:    binDst,
		Settings:  settingsPath,
		PIDFile:   filepath.Join(dir, "agent.pid"),
		TokenFile: tokenFile,
		SlotsPath: *slotsPath,
		Created:   store.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		return fail("slot setup: %v", err)
	}
	if err := writeFileExcl(filepath.Join(dir, "slot.json"), rawManifest, 0o600); err != nil {
		return fail("slot setup: %v", err)
	}
	created = append(created, filepath.Join(dir, "slot.json"))
	if err := store.SetDir(server.Name, slot.ID, *holder, leaseID, dir); err != nil {
		if r, ok := err.(*slots.Refusal); ok {
			cleanup()
			fmt.Fprintf(stderr, "slot setup: %s\n", r.Msg)
			return exitSlotRefused
		}
		return fail("slot setup: %v", err)
	}
	fmt.Fprintf(stdout, "slot %s/%s ready in %s\n", server.Name, slot.ID, dir)
	fmt.Fprintf(stdout, "account %s, character %s, port %d\n", slot.Account, character, slot.Port)
	fmt.Fprintf(stdout, "start: nohup %s login --config %s --pid-file %s %s > %s 2>&1 &\n",
		binDst, settingsPath, manifest.PIDFile, character, filepath.Join(dir, "agent.out"))
	fmt.Fprintf(stdout, "rpc: 127.0.0.1:%d, token file %s (written at login)\n", slot.Port, tokenFile)
	fmt.Fprintf(stdout, "teardown: %s teardown %s\n", binDst, dir)
	return exitOK
}

// slotSetupCheckNoSymlink refuses a --dir that is a symlink or sits under
// one: writes through a symlink land outside the claimed dir.
func slotSetupCheckNoSymlink(dir string) error {
	clean := filepath.Clean(dir)
	for p := clean; ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("slot setup: %s resolves through a symlink: pick a new --dir (setup never writes through a symlink)", clean)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("slot setup: %v", err)
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
	}
	// Belt and braces: the deepest existing ancestor must resolve to
	// itself, or a symlink component hid from the walk above.
	existing := clean
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	if real, err := filepath.EvalSymlinks(existing); err == nil && real != filepath.Clean(existing) {
		return fmt.Errorf("slot setup: %s resolves through a symlink: pick a new --dir (setup never writes through a symlink)", clean)
	}
	return nil
}

// slotSetupEnsureDir MkdirAlls path, reporting whether setup created it.
// Only setup-created paths are tracked for cleanup: a pre-existing
// caller-supplied dir (e.g. an external --dungeons-dir) is never removed.
func slotSetupEnsureDir(path string, mode os.FileMode) (bool, error) {
	if st, err := os.Stat(path); err == nil {
		if !st.IsDir() {
			return false, fmt.Errorf("slot setup: %s exists and is not a dir", path)
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return false, err
	}
	return true, nil
}

// slotSetupInsideDir reports whether p is dir itself or below it.
func slotSetupInsideDir(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func slotSetupCheckLease(store *slots.Store, server, slot, holder string) (string, error) {
	states, err := store.List(server)
	if err != nil {
		return "", err
	}
	for _, s := range states {
		if s.Slot.ID != slot {
			continue
		}
		if s.State == "held" && s.Lease != nil && s.Lease.Holder == holder {
			return s.Lease.LeaseID, nil
		}
		return "", fmt.Errorf("not leased by %s", holder)
	}
	return "", fmt.Errorf("not leased by %s", holder)
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

// slotSetupWriteSettings writes the client settings via tmp+rename (0600),
// then loads them back; a load failure removes the file. The dir was claimed
// empty, so the rename never overwrites.
func slotSetupWriteSettings(path string, dst config.Settings, stateDir string) error {
	raw, err := json.MarshalIndent(dst, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return err
	}
	if _, err := config.Load(tmpName); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
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
