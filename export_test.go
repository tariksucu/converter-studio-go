package converterstudio

import (
	"context"
	"time"
)

// What the black-box tests reach inside the package.

// SetSleep replaces the pauses between attempts and between status requests.
func SetSleep(c *Client, sleep func(context.Context, time.Duration) error) { c.sleep = sleep }

var (
	RetryDelay   = retryDelay
	RetryAfter   = retryAfter
	PollBackoff  = pollBackoff
	SafeFileName = safeFileName
	PartRanges   = partRanges
	UserAgent    = defaultUserAgent
)
