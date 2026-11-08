// instancecap.go: the per-server core cap of fresh instance ids per account and hour
// (MAX_INSTANCE_PER_ACCOUNT_PER_HOUR). It lives in its own file beside the slot table, not in slots.json: older
// binaries read slots.json with unknown fields refused, so a field there would break them. The raid commands read it
// unless their --instance-cap-per-account flag is given; a missing file means the stock core's 5 everywhere.
package slots

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// InstanceCapPerAccountFile is the file name of the per-server caps in the slot table's folder.
const InstanceCapPerAccountFile = "instance-cap-per-account.json"

// LoadInstanceCaps reads the per-server caps: {"m910q": 30, "tortoise-test2": 30, "tortoise-test3": 30}. A missing file is no caps (nil).
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
