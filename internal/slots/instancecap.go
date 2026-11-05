// instancecap.go: the per-server core cap of fresh instance ids per account and hour
// (MAX_INSTANCE_PER_ACCOUNT_PER_HOUR). It lives in its own file beside the slot table, not in slots.json: older
// binaries read slots.json with unknown fields refused, so a field there would break them. A missing file means the
// stock core's 5 everywhere.
package slots

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// InstanceCapPerAccountFile is the file name of the per-server caps in the slot table's folder.
const InstanceCapPerAccountFile = "instance-cap-per-account.json"

// StockInstanceCap is the stock core's cap per pool account: the 6th distinct instance id in an hour is refused.
const StockInstanceCap = 5

// LoadInstanceCaps reads the per-server caps: {"m910q": 30, "tortoise-test2": 30}. A missing file is no caps (nil).
// A value below 1 is refused. An unreadable or invalid file is an error, never "no caps".
func LoadInstanceCaps(path string) (map[string]int, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var caps map[string]int
	if err := json.Unmarshal(raw, &caps); err != nil {
		return nil, fmt.Errorf("instance caps %s: %w", path, err)
	}
	for server, n := range caps {
		if n < 1 {
			return nil, fmt.Errorf("instance caps %s: server %q: the cap must be 1 or more, not %d", path, server, n)
		}
	}
	return caps, nil
}

// EffectiveInstanceCap resolves a server's cap from the sources in precedence order: an explicit flag, else the
// server's entry in instance-cap-per-account.json, else the stock core's 5. walk-preflight calls it so its printed cap
// follows the same rule as the raid commands.
func EffectiveInstanceCap(explicit int, caps map[string]int, server string) int {
	if explicit > 0 {
		return explicit
	}
	if n, ok := caps[server]; ok && n > 0 {
		return n
	}
	return StockInstanceCap
}
