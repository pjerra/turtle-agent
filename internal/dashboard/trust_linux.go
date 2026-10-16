//go:build linux

package dashboard

import (
	"bufio"
	"encoding/hex"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// socketOwner finds the user the TCP socket whose local end is client belongs to, from the kernel's tables. client is the address the dashboard
// sees as the peer of a local connection, which is the local address of the caller's own socket. It answers false when the socket is not found.
func socketOwner(client netip.AddrPort) (uint32, bool) {
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if uid, ok := scanProcNet(f, client); ok {
			return uid, true
		}
	}
	return 0, false
}

// Socket states that belong to a live connection: ESTABLISHED, FIN_WAIT1, FIN_WAIT2, CLOSE_WAIT (a client that has sent its request and closed its
// write side). LISTEN and TIME_WAIT rows are left out (a TIME_WAIT row has no owner).
var liveStates = map[string]bool{"01": true, "04": true, "05": true, "08": true}

func scanProcNet(path string, want netip.AddrPort) (uint32, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	first := true
	for sc.Scan() {
		if first { // the header
			first = false
			continue
		}
		fl := strings.Fields(sc.Text())
		if len(fl) < 8 || !liveStates[fl[3]] {
			continue
		}
		la, ok := parseProcAddr(fl[1])
		if !ok || la != want {
			continue
		}
		uid, err := strconv.ParseUint(fl[7], 10, 32)
		if err != nil {
			return 0, false
		}
		return uint32(uid), true
	}
	return 0, false
}

// parseProcAddr reads an address of /proc/net/tcp{,6}: hex words in host (little-endian) order, a colon, the port in hex.
func parseProcAddr(s string) (netip.AddrPort, bool) {
	ip, port, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, false
	}
	b, err := hex.DecodeString(ip)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return netip.AddrPort{}, false
	}
	for i := 0; i+3 < len(b); i += 4 { // each 32-bit word is stored reversed
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	p, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	a, ok := netip.AddrFromSlice(b)
	if !ok {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(a.Unmap(), uint16(p)), true
}
