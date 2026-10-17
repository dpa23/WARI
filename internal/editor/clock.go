package editor

import (
	"time"
)

// monoNow returns the current time as nanoseconds (monotonic clock).
func monoNow() int64 {
	return time.Now().UnixNano()
}
