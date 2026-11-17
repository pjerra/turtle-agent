package rpc

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/privfile"
)

// NewToken returns a random bearer token: 32 bytes from crypto/rand, base64url (43 characters).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// TokenFile is where an agent listening on port keeps its generated token: next to the
// settings file, one file per port so a fleet's children do not share one.
func TokenFile(settingsPath string, port int) string {
	return filepath.Join(filepath.Dir(settingsPath), "rpc-"+strconv.Itoa(port)+".token")
}

// WriteTokenFile stores token in path with mode 0600 and replaces a stale file left by an
// earlier run. The new file is complete and private before it takes the name; a folder
// that anyone can write to is refused, since another user could swap the file there. The
// returned func removes the file again, but only while it still holds this token: an agent
// restarted on the same port has replaced it by then, and its file must stay.
func WriteTokenFile(path, token string) (remove func(), err error) {
	dir := filepath.Dir(path)
	if info, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("rpc token file: %w", err)
	} else if !info.IsDir() || info.Mode().Perm()&0o002 != 0 {
		return nil, fmt.Errorf("rpc token file: %s must be a folder that is not world-writable", dir)
	}
	tmp := path + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("rpc token file: %w", err)
	}
	_, err = f.WriteString(token + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("rpc token file: %w", err)
	}
	return func() {
		if cur, err := ReadTokenFile(path); err == nil && subtle.ConstantTimeCompare([]byte(cur), []byte(token)) == 1 {
			os.Remove(path)
		}
	}, nil
}

// ReadTokenFile returns the token in path. It refuses anything but a regular file (a
// symlink is not followed) that only its owner can access, and an empty one.
func ReadTokenFile(path string) (string, error) {
	raw, err := privfile.Read(path, 4096)
	if err != nil {
		return "", fmt.Errorf("rpc token file: %w", err)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", errors.New("rpc token file is empty")
	}
	return tok, nil
}

// tokenMatch compares the header value with the token in constant time. Both sides are
// hashed first so the comparison does not depend on their lengths either.
func tokenMatch(header string, want [sha256.Size]byte) bool {
	const scheme = "Bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return false
	}
	got := sha256.Sum256([]byte(header[len(scheme):]))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}
