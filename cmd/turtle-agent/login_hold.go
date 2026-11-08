package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

// loginHourHoldEnv turns the realmd hour-start hold off when set to 0. The hold is on by default.
const loginHourHoldEnv = "TA_LOGIN_HOUR_HOLD"

// loginHourHoldEnd is how far into each UTC hour the hold lasts. realmd's 15-accounts-per-IP throttle
// (AuthSocket.cpp:439-457) compares last_login > NOW() - '300' as numeric date maths; in minutes :00-:02 of every hour that
// counts every account ever seen from the IP (behind docker NAT every outside client shares one IP), so logins are refused
// with code 0x08. The 5 s past :03 is margin.
const loginHourHoldEnd = 3*time.Minute + 5*time.Second

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
)

// hourStartHold is how long a realmd login starting at now must wait: until UTC hh:03:05 when it falls
// between hh:00:00 and hh:03:05, else 0.
func hourStartHold(now time.Time) time.Duration {
	u := now.UTC()
	into := time.Duration(u.Minute())*time.Minute + time.Duration(u.Second())*time.Second + time.Duration(u.Nanosecond())
	if into >= loginHourHoldEnd {
		return 0
	}
	return loginHourHoldEnd - into
}

// holdLoginForHourStart blocks until realmd's hour-start window is over. Every realmd login (the login command and its
// reconnects, so raid-walk, raid-autopilot, lease and slot setup too) calls it before the handshake. It logs one line
// when it waits and returns the context's error if the wait is cut off.
func holdLoginForHourStart(ctx context.Context, stderr io.Writer) error {
	if os.Getenv(loginHourHoldEnv) == "0" {
		return nil
	}
	d := hourStartHold(loginHoldNow())
	if d <= 0 {
		return nil
	}
	secs := int((d + time.Second - 1) / time.Second)
	fmt.Fprintf(stderr, "login: waiting %d s for the realmd hour-start window\n", secs)
	return loginHoldSleep(ctx, d)
}
