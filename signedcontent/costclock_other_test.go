//go:build !unix

// The gateway relay's cost-row clock (its costclock_other_test.go), carried with the reading's
// cost test.

package signedcontent

import "time"

var costEpoch = time.Now()

// cpuTime is the wall clock where the process's processor time is not read
// (costclock_unix_test.go).
func cpuTime() time.Duration { return time.Since(costEpoch) }
