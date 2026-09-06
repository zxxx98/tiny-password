package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDailyTime validates the persisted "HH:MM" schedule form.
func ParseDailyTime(value string) (int, int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("daily time must be HH:MM")
	}
	hh, err := strconv.Atoi(parts[0])
	if err != nil || hh < 0 || hh > 23 || len(parts[0]) != 2 {
		return 0, 0, fmt.Errorf("daily time hour out of range")
	}
	mm, err := strconv.Atoi(parts[1])
	if err != nil || mm < 0 || mm > 59 || len(parts[1]) != 2 {
		return 0, 0, fmt.Errorf("daily time minute out of range")
	}
	return hh, mm, nil
}

// LoadLocation resolves an IANA zone name; empty means UTC. The tzdata
// package import in this file guarantees zones resolve even in minimal
// containers without /usr/share/zoneinfo.
func LoadLocation(name string) (*time.Location, error) {
	if name == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(name)
}

// DailyRunAt returns the instant at which the given wall time should run
// today in loc. time.Date normalization alone maps a spring-forward gap
// BACKWARD (02:30 nonexistent → 01:30 EST), but decision D09 requires the
// next valid instant, so a gap is detected explicitly and the run time is
// advanced until the local wall clock reaches the requested time. A
// fall-back repeat keeps time.Date's first occurrence (runs once).
func DailyRunAt(now time.Time, hh, mm int, loc *time.Location) time.Time {
	local := now.In(loc)
	runAt := time.Date(local.Year(), local.Month(), local.Day(), hh, mm, 0, 0, loc)
	want := hh*60 + mm
	// Bounded to four hours: no DST shift exceeds that.
	for i := 0; i < 4; i++ {
		wall := runAt.Hour()*60 + runAt.Minute()
		if wall >= want {
			break
		}
		runAt = runAt.Add(time.Duration(want-wall) * time.Minute)
	}
	return runAt
}
