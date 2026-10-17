//go:build !linux

package dashboard

import "net/netip"

// socketOwner cannot tell whose a socket is off Linux, so a local caller is never trusted there (the token is asked for).
func socketOwner(netip.AddrPort) (uint32, bool) { return 0, false }
