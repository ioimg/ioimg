//go:build !vips

package imaging

import "time"

// timeNowUnixNano is a variable so tests can freeze the clock if needed.
var timeNowUnixNano = func() int64 { return time.Now().UnixNano() }
