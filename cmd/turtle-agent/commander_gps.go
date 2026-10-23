package main

import (
	"context"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// The `.gps` read of the start's z check (dungeon.StartHands.GPS). `.gps` is a plain GM line (no ".bot " in front) and the core answers it
// with several system messages; the one with GroundZ and FloorZ ends the read (a TileX line may follow it).
const (
	gpsTotalWait = 3 * time.Second        // the whole read
	gpsQuietWait = 400 * time.Millisecond // the read ends this long after the last line once a line has come
)

// GPS selects the agent's own character, waits until the server shows it selected, and sends ".gps": the core reports the unit the GM has
// selected, so a mob or a bot left selected would be read in the commander's place. It returns the system lines that followed, joined by
// newlines. An error is a selection or a line that could not be sent, or no reply within gpsTotalWait.
func (g remoteGame) GPS(ctx context.Context) (string, error) {
	log := g.c.cur().Log()
	since := log.Last()
	if err := g.selectThenGM(ctx, g.AgentGUID(), ".gps"); err != nil {
		return "", err
	}
	return readGPSReply(ctx, log, since, gpsTotalWait, gpsQuietWait)
}

// readGPSReply collects the system lines the log gets after since: it returns at the line that has "FloorZ" (the one that ends the reply),
// else quiet after the last line once one has come, else at total with an error when there was none.
func readGPSReply(ctx context.Context, log *events.Log, since uint64, total, quiet time.Duration) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()
	var lines []string
	cursor := since
	for {
		p := log.Read(events.Query{Since: cursor, Kinds: []string{events.KindSystem}, Limit: events.MaxLimit})
		for _, e := range p.Events {
			if e.Text == "" {
				continue
			}
			lines = append(lines, e.Text)
			if strings.Contains(e.Text, "FloorZ") {
				return strings.Join(lines, "\n"), nil
			}
		}
		cursor = p.Next
		if p.More {
			continue
		}
		wctx, cancel := tctx, context.CancelFunc(func() {})
		if len(lines) > 0 {
			wctx, cancel = context.WithTimeout(tctx, quiet)
		}
		err := log.Wait(wctx, cursor)
		cancel()
		if err != nil {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if len(lines) == 0 {
				return "", context.DeadlineExceeded
			}
			return strings.Join(lines, "\n"), nil
		}
	}
}
