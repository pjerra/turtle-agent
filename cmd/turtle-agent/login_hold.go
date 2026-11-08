package main

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"time"
)

// loginHourHoldEnv turns the realmd hour-start hold off when set to 0. The hold is on by default.
const loginHourHoldEnv = "TA_LOGIN_HOUR_HOLD"

const (
	// loginHourHoldStart is how far into each UTC hour the hold window begins, counted back from the hour: 3 s early, for clock
	// skew between this host and the database's NOW(). The window assumes that NOW() uses a whole-hour time zone.
	loginHourHoldStart = 3 * time.Second
	// loginHourHoldEnd is how far into each UTC hour the hold lasts. realmd's 15-accounts-per-IP throttle
	// (AuthSocket.cpp:439-457) compares last_login > NOW() - '300' as numeric date maths; in minutes :00-:02 of every hour that
	// counts every account ever seen from the IP (behind docker NAT every outside client shares one IP), so logins are refused
	// with code 0x08. The 5 s past :03 is margin.
	loginHourHoldEnd = 3*time.Minute + 5*time.Second
	// loginHoldJitterMax is the most a held login adds after the window, so the logins that were held do not all fire at once.
	loginHoldJitterMax = 20 * time.Second
)

var (
	loginHoldNow   = time.Now
	loginHoldSleep = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// loginHoldJitter is the per-process extra wait after a hold, 0 to loginHoldJitterMax. Tests replace it.
	loginHoldJitter = func() time.Duration { return rand.N(loginHoldJitterMax + 1) }
)

// loginHoldEnabled is false when TA_LOGIN_HOUR_HOLD=0.
func loginHoldEnabled() bool { return os.Getenv(loginHourHoldEnv) != "0" }

// hourStartHold is how long a realmd login starting at now must wait: until UTC hh:03:05 when it falls
// between hh:59:57 of the hour before and hh:03:05, else 0. It does not look at the env switch.
func hourStartHold(now time.Time) time.Duration {
	u := now.UTC()
	into := time.Duration(u.Minute())*time.Minute + time.Duration(u.Second())*time.Second + time.Duration(u.Nanosecond())
	if into >= loginHourHoldEnd {
		if into >= time.Hour-loginHourHoldStart {
			return time.Hour - into + loginHourHoldEnd
		}
		return 0
	}
	return loginHourHoldEnd - into
}

// loginHold is hourStartHold when the hold is on, else 0.
func loginHold(now time.Time) time.Duration {
	if !loginHoldEnabled() {
		return 0
	}
	return hourStartHold(now)
}

// loginHoldExtra is the most a login child that starts at now can spend in the hold and its jitter: what a parent adds to the
// time it gives the child to reach the world.
func loginHoldExtra(now time.Time) time.Duration {
	if h := loginHold(now); h > 0 {
		return h + loginHoldJitterMax
	}
	return 0
}

// loginDeadline is when a parent that started a login child at now gives up on it: base after the start, plus the hold the child
// sits in when it starts inside realmd's hour-start window.
func loginDeadline(now time.Time, base time.Duration) time.Time {
	return now.Add(base + loginHoldExtra(now))
}

// holdLoginForHourStart blocks until realmd's hour-start window is over, then adds a small per-process jitter. Every realmd login (the
// login command and its reconnects, so raid-walk, raid-autopilot, lease and slot setup too) calls it before the handshake. It logs one
// line when it waits and returns the context's error if the wait is cut off.
func holdLoginForHourStart(ctx context.Context, stderr io.Writer) error {
	d := loginHold(loginHoldNow())
	if d <= 0 {
		return nil
	}
	secs := int((d + time.Second - 1) / time.Second)
	fmt.Fprintf(stderr, "login: waiting %d s for the realmd hour-start window\n", secs)
	if err := loginHoldSleep(ctx, d); err != nil {
		return err
	}
	if j := loginHoldJitter(); j > 0 {
		return loginHoldSleep(ctx, j)
	}
	return nil
}
