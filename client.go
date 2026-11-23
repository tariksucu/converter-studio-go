package converterstudio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the address of the Converter Studio API.
	DefaultBaseURL = "https://converter.studio/api"
	// APIKeyEnv is the environment variable the API key is read from when [WithAPIKey] is not
	// given.
	APIKeyEnv = "CONVERTER_STUDIO_API_KEY"
	// BaseURLEnv is the environment variable the API's address is read from when
	// [WithBaseURL] is not given.
	BaseURLEnv = "CONVERTER_STUDIO_BASE_URL"
	// DefaultTimeout is how long a request may go without progress ([WithTimeout]).
	DefaultTimeout = 60 * time.Second
	// DefaultMaxRetries is how often a request is sent again ([WithMaxRetries]).
	DefaultMaxRetries = 2
	// DefaultWaitTimeout is how long [Client.Wait] waits for a conversion ([WithWaitTimeout]).
	DefaultWaitTimeout = 10 * time.Minute

	clientName   = "go"
	clientHeader = "X-Converter-Studio-Client"
	// The API holds a status request up to this many seconds until the conversion settles.
	longPollSeconds = 25
	// One request's body at most (the CDN in front of the API stops at 100 MB).
	defaultPartBytes = 95 << 20
)

// Client is a client for the Converter Studio API. Create one with [NewClient]; it is safe
// for use by several goroutines at once.
type Client struct {
	apiKey      string
	keySet      bool
	baseURL     string
	http        *http.Client // uploads and downloads (redirects followed)
	api         *http.Client // the API itself (redirects not followed)
	userAgent   string
	timeout     time.Duration
	maxRetries  int
	waitTimeout time.Duration
	// sleep pauses between attempts and status requests; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
}

// An Option configures a [Client].
type Option func(*Client) error

// WithAPIKey sets the account's API key (keys start with "cvs_live_"). Without this option,
// the key is read from CONVERTER_STUDIO_API_KEY. Listing formats needs no key.
func WithAPIKey(key string) Option {
	return func(c *Client) error {
		c.apiKey, c.keySet = strings.TrimSpace(key), true
		return nil
	}
}

// WithBaseURL sets the API's address. Without this option, it is read from
// CONVERTER_STUDIO_BASE_URL, or else it is [DefaultBaseURL].
func WithBaseURL(baseURL string) Option {
	return func(c *Client) error {
		u, err := checkBaseURL(baseURL)
		if err != nil {
			return err
		}
		c.baseURL = u
		return nil
	}
}

// WithHTTPClient makes the client send its requests with hc (for a proxy, your own
// certificates or tracing). hc should have no Timeout: the API holds status requests for up
// to a minute, and large files take longer than that; use [WithTimeout] and contexts instead.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) error {
		if hc == nil {
			return errors.New("converterstudio: WithHTTPClient needs an *http.Client")
		}
		c.http = hc
		return nil
	}
}

// WithUserAgent puts your application's name and version (such as "my-app/1.2") in front of
// the client's own User-Agent header.
func WithUserAgent(product string) Option {
	return func(c *Client) error {
		product = strings.TrimSpace(product)
		if strings.ContainsAny(product, "\r\n") {
			return errors.New("converterstudio: the user agent must be on one line")
		}
		if product != "" {
			c.userAgent = product + " " + defaultUserAgent()
		}
		return nil
	}
}

// WithTimeout sets how long a request may go without progress: to connect and get an answer,
// or between two pieces of a file sent or received ([DefaultTimeout] by default). It is not a
// limit on a whole call, which may send or receive a large file: use the context for that.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) error {
		if d <= 0 {
			return errors.New("converterstudio: the timeout must be more than 0")
		}
		c.timeout = d
		return nil
	}
}

// WithMaxRetries sets how often a request is sent again after a network error or a 408, 429
// or 5xx answer ([DefaultMaxRetries] by default; 0 turns retries off). The pauses grow (half a
// second, doubling up to 8 seconds) or follow the API's Retry-After. Conversions are created
// with an Idempotency-Key, so a retry never starts or charges a second one.
func WithMaxRetries(n int) Option {
	return func(c *Client) error {
		if n < 0 {
			return errors.New("converterstudio: the number of retries must be 0 or more")
		}
		c.maxRetries = n
		return nil
	}
}

// WithWaitTimeout sets how long [Client.Wait] and the Convert methods wait for a conversion
// to finish ([DefaultWaitTimeout] by default; 0: as long as the context allows).
func WithWaitTimeout(d time.Duration) Option {
	return func(c *Client) error {
		if d < 0 {
			return errors.New("converterstudio: the wait timeout must be 0 or more")
		}
		c.waitTimeout = d
		return nil
	}
}

// NewClient returns a client for the Converter Studio API. Without options it reads the API
// key from CONVERTER_STUDIO_API_KEY and talks to https://converter.studio/api.
//
// NewClient fails only on an invalid option or an invalid CONVERTER_STUDIO_BASE_URL. A missing
// API key is reported by the calls that need one, as [ErrMissingAPIKey].
func NewClient(opts ...Option) (*Client, error) {
	c := &Client{
		userAgent:   defaultUserAgent(),
		timeout:     DefaultTimeout,
		maxRetries:  DefaultMaxRetries,
		waitTimeout: DefaultWaitTimeout,
		sleep:       sleepContext,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	if !c.keySet {
		c.apiKey = strings.TrimSpace(os.Getenv(APIKeyEnv))
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
		if env := strings.TrimSpace(os.Getenv(BaseURLEnv)); env != "" {
			u, err := checkBaseURL(env)
			if err != nil {
				return nil, fmt.Errorf("%w (from %s)", err, BaseURLEnv)
			}
			c.baseURL = u
		}
	}
	if c.http == nil {
		c.http = &http.Client{Transport: http.DefaultTransport}
		if t, ok := http.DefaultTransport.(*http.Transport); ok {
			c.http.Transport = t.Clone()
		}
	}
	api := *c.http
	api.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.api = &api
	return c, nil
}

// BaseURL returns the API's address the client talks to.
func (c *Client) BaseURL() string { return c.baseURL }

func checkBaseURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("converterstudio: the base URL must be an http:// or https:// address, not %q", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("converterstudio: the base URL must not hold a query, a fragment, a user name or a password")
	}
	return trimmed, nil
}

func defaultUserAgent() string {
	return fmt.Sprintf("converter-studio-go/%s (%s; %s/%s)", Version, runtime.Version(),
		runtime.GOOS, runtime.GOARCH)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
