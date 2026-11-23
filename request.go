package converterstudio

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"
)

// A call is one API request, sent again by [Client.retry] when that is safe.
type call struct {
	method string
	url    string // absolute, with its query
	body   any    // sent as JSON
	part   *part  // or the bytes of an upload part
	header map[string]string
	auth   bool          // send the API key (only ever to the API's own address)
	hold   time.Duration // how long the API may hold the answer (long polling)
	follow bool          // follow redirects (downloads)
	// partDone: a 404 after the first attempt means an earlier attempt got there (an upload
	// slot is gone once its last part is in).
	partDone bool
}

type part struct {
	src    io.ReaderAt
	offset int64
	length int64
}

// The most a JSON answer may hold.
const maxJSONBytes = 16 << 20

var errNoProgress = errors.New("no progress within the timeout")

// finalError wraps an error that must not be retried (a download that already wrote to the
// caller's writer).
type finalError struct{ err error }

func (e *finalError) Error() string { return e.err.Error() }
func (e *finalError) Unwrap() error { return e.err }

func (c *Client) apiURL(path string, query url.Values) string {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

// callJSON sends k and returns the answer's body and header.
func (c *Client) callJSON(ctx context.Context, k *call) ([]byte, http.Header, error) {
	var data []byte
	var header http.Header
	err := c.retry(ctx, k, func(resp *http.Response) error {
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
		if err != nil {
			return err
		}
		data, header = b, resp.Header
		return nil
	})
	return data, header, err
}

// decodeJSON decodes an answer of the API into out.
func decodeJSON(data []byte, out any) error {
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("converterstudio: unreadable answer from the API: " + err.Error())
	}
	return nil
}

// retry sends k and hands a successful answer to read, again after network errors (also
// while read reads the body) and 408, 429 and 5xx answers, up to the client's retries. The
// answer's body is closed after read.
func (c *Client) retry(ctx context.Context, k *call, read func(*http.Response) error) error {
	if k.auth && c.apiKey == "" {
		return ErrMissingAPIKey
	}
	var data []byte
	if k.body != nil {
		var err error
		if data, err = json.Marshal(k.body); err != nil {
			return errors.New("converterstudio: the request cannot be sent as JSON: " + err.Error())
		}
	}
	for attempt := 0; ; attempt++ {
		last := attempt >= c.maxRetries
		resp, err := c.send(ctx, k, data)
		var wait time.Duration
		switch {
		case err != nil:
			if last || !retryable(err) {
				return err
			}
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			err = read(resp)
			_ = resp.Body.Close()
			var final *finalError
			if err == nil {
				return nil
			} else if errors.As(err, &final) {
				return final.err
			} else if last || !retryable(err) {
				return err
			}
		default:
			apiErr := errorFromResponse(resp)
			if k.partDone && attempt > 0 && apiErr.StatusCode == http.StatusNotFound {
				return nil
			}
			if last || !retryStatus(apiErr.StatusCode) {
				return apiErr
			}
			wait = apiErr.RetryAfter
		}
		if err := c.sleep(ctx, retryDelay(attempt, wait)); err != nil {
			return err
		}
	}
}

// send makes one attempt. A timer cancels it when it goes without progress for the client's
// timeout (plus k.hold while the API holds the answer); every piece of the body sent or
// received restarts the timer.
func (c *Client) send(ctx context.Context, k *call, data []byte) (*http.Response, error) {
	attemptCtx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(c.timeout+k.hold, func() { cancel(errNoProgress) })
	kick := func() { timer.Reset(c.timeout) }
	stop := func() {
		timer.Stop()
		cancel(nil)
	}

	var newBody func() io.Reader
	var length int64
	switch {
	case k.part != nil:
		p := k.part
		length = p.length
		newBody = func() io.Reader {
			return &progressReader{r: io.NewSectionReader(p.src, p.offset, p.length), kick: kick}
		}
	case data != nil:
		length = int64(len(data))
		newBody = func() io.Reader { return bytes.NewReader(data) }
	}
	req, err := http.NewRequestWithContext(attemptCtx, k.method, k.url, nil)
	if err != nil {
		stop()
		return nil, errors.New("converterstudio: cannot make a request: " + err.Error())
	}
	if newBody != nil {
		req.ContentLength = length
		if length == 0 {
			req.Body = http.NoBody
			req.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
		} else {
			req.Body = io.NopCloser(newBody())
			req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(newBody()), nil }
		}
	}
	req.Header.Set(clientHeader, clientName+"/"+Version)
	req.Header.Set("User-Agent", c.userAgent)
	switch {
	case k.part != nil:
		req.Header.Set("Content-Type", "application/octet-stream")
	case data != nil:
		req.Header.Set("Content-Type", "application/json")
	}
	if !k.follow {
		req.Header.Set("Accept", "application/json")
	}
	for name, value := range k.header {
		req.Header.Set(name, value)
	}
	if k.auth {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	hc := c.api
	if k.follow {
		hc = c.http
	}
	resp, err := hc.Do(req)
	if err != nil {
		stop()
		return nil, networkError(ctx, attemptCtx, req.URL.Host, err)
	}
	resp.Body = &progressBody{
		rc: resp.Body, kick: kick, stop: stop, ctx: ctx, attemptCtx: attemptCtx, host: req.URL.Host,
	}
	return resp, nil
}

// networkError is the caller's context error when it ended, and otherwise a
// [ConnectionError] that names only the host (links carry tokens in their query).
func networkError(ctx, attemptCtx context.Context, host string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if context.Cause(attemptCtx) == errNoProgress {
		return &ConnectionError{Host: host, Err: errNoProgress, timeout: true}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return &ConnectionError{Host: host, Err: err}
}

// retryable reports whether an attempt that failed with err may be sent again: network errors
// may, but not a certificate that does not verify, a context that ended or a refused file.
func retryable(err error) bool {
	var connErr *ConnectionError
	if !errors.As(err, &connErr) {
		return false
	}
	var certErr *tls.CertificateVerificationError
	return !errors.As(connErr.Err, &certErr)
}

func retryStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// retryDelay is the pause before retry attempt+1: the API's Retry-After (at most a minute),
// or half a second doubling up to 8 seconds, with jitter.
func retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, time.Minute)
	}
	base := 8 * time.Second
	if attempt < 4 {
		base = (500 * time.Millisecond) << attempt
	}
	return base/2 + rand.N(base/2)
}

// progressReader restarts the attempt's timer whenever a piece of an upload is read.
type progressReader struct {
	r    io.Reader
	kick func()
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.kick()
	}
	return n, err
}

// progressBody restarts the attempt's timer whenever a piece of the answer arrives, turns
// read errors into [ConnectionError]s and stops the timer when closed.
type progressBody struct {
	rc         io.ReadCloser
	kick, stop func()
	ctx        context.Context
	attemptCtx context.Context
	host       string
}

func (b *progressBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.kick()
	}
	if err != nil && err != io.EOF {
		err = networkError(b.ctx, b.attemptCtx, b.host, err)
	}
	return n, err
}

func (b *progressBody) Close() error {
	err := b.rc.Close()
	b.stop()
	return err
}
