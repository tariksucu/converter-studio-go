package converterstudio

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ConversionParams are the settings of [Client.CreateConversion].
type ConversionParams struct {
	// To is the format to convert to ("jpg", "mp4", "pdf"...). Required.
	To string
	// URL is a link to the file (a direct link, or a Google Drive, Dropbox or OneDrive share
	// link): Converter Studio fetches it itself. Give either URL or UploadID.
	URL string
	// UploadID is the ID of an upload ([Client.UploadFile], [Client.UploadReader]).
	UploadID string
	// Options are the conversion's settings, the same as on the website, such as
	// {"quality": "high"} or {"height": "720"}. Settings that do not apply to a conversion are
	// ignored; a name the API does not know is refused ([ErrBadRequest]). See
	// https://converter.studio/developers for the list.
	Options map[string]any
	// MaxCredits is the price up to which the conversion goes ahead on its own (0: the API's
	// default, 20). A dearer one waits for [Client.Confirm] and costs nothing until then.
	MaxCredits int
	// QuoteOnly only gets the price: the conversion waits for [Client.Confirm] whatever it
	// costs (it sends a maximum of 0 credits).
	QuoteOnly bool
	// CallbackURL is an https:// address on the public internet that Converter Studio POSTs
	// to when the conversion waits for a go-ahead ("conversion.quoted") and when it ends
	// ("conversion.done", "conversion.failed" or "conversion.canceled"). Callbacks are not
	// signed: keep the address secret, and check the conversion with [Client.GetConversion]
	// before acting on one.
	CallbackURL string
	// IdempotencyKey makes a repeated request (within 24 hours) return the first conversion
	// instead of starting and charging a second one; the same key with a different request
	// fails with [ErrConflict]. When empty, the client makes one up for this call and keeps it
	// for its own retries. Pass your own when your code itself may repeat a request (a job
	// queue retrying a task). 1 to 255 visible ASCII characters.
	IdempotencyKey string
}

// ConvertParams are the settings of [Client.ConvertFile], [Client.ConvertReader] and
// [Client.ConvertURL]. The zero value (or nil) converts with the default settings and waits.
type ConvertParams struct {
	// Options, MaxCredits, QuoteOnly, CallbackURL and IdempotencyKey are those of
	// [ConversionParams].
	Options        map[string]any
	MaxCredits     int
	QuoteOnly      bool
	CallbackURL    string
	IdempotencyKey string
	// NoWait returns as soon as the conversion has started, without waiting for it.
	NoWait bool
	// PartSize makes the parts of a large upload smaller (see [UploadOptions]).
	PartSize int64
}

// CreateConversion starts converting a link's file or an upload (POST /v1/conversions) and
// returns at once; see [Client.Wait]. It carries an Idempotency-Key, so the client's own
// retries never start or charge a second conversion.
func (c *Client) CreateConversion(ctx context.Context, p ConversionParams) (*Conversion, error) {
	body, err := conversionBody(p)
	if err != nil {
		return nil, err
	}
	key := p.IdempotencyKey
	if key == "" {
		key = newIdempotencyKey()
	} else if !validIdempotencyKey(key) {
		return nil, errors.New("converterstudio: the idempotency key must be 1 to 255 visible ASCII characters")
	}
	k := &call{
		method: http.MethodPost, url: c.apiURL("/v1/conversions", nil), body: body, auth: true,
		header: map[string]string{"Idempotency-Key": key},
	}
	data, header, err := c.callJSON(ctx, k)
	if err != nil {
		return nil, err
	}
	conversion, err := decodeConversion(data)
	if err != nil {
		return nil, err
	}
	conversion.Replayed = strings.EqualFold(header.Get("Idempotent-Replayed"), "true")
	return conversion, nil
}

// GetConversion returns a conversion's state now (GET /v1/conversions/{id}).
func (c *Client) GetConversion(ctx context.Context, id string) (*Conversion, error) {
	return c.getConversion(ctx, id, 0)
}

// WaitForUpdate is [Client.GetConversion], but the API holds the answer until the conversion
// is done, failed or waits for a go-ahead, or until maxWait (1 to 60 seconds) has passed.
func (c *Client) WaitForUpdate(ctx context.Context, id string, maxWait time.Duration) (*Conversion, error) {
	seconds := int(maxWait / time.Second)
	return c.getConversion(ctx, id, max(1, min(seconds, 60)))
}

func (c *Client) getConversion(ctx context.Context, id string, wait int) (*Conversion, error) {
	path, err := conversionPath(id)
	if err != nil {
		return nil, err
	}
	k := &call{method: http.MethodGet, auth: true}
	if wait > 0 {
		k.url = c.apiURL(path, url.Values{"wait": {strconv.Itoa(wait)}})
		k.hold = time.Duration(wait) * time.Second
	} else {
		k.url = c.apiURL(path, nil)
	}
	return c.conversionCall(ctx, k)
}

// Confirm goes ahead with a conversion that waits for a go-ahead ([StatusQuoted]): it costs
// more than its maximum, or it was created with QuoteOnly.
func (c *Client) Confirm(ctx context.Context, id string) (*Conversion, error) {
	path, err := conversionPath(id)
	if err != nil {
		return nil, err
	}
	return c.conversionCall(ctx, &call{method: http.MethodPost, url: c.apiURL(path+"/confirm", nil), auth: true})
}

func (c *Client) conversionCall(ctx context.Context, k *call) (*Conversion, error) {
	data, _, err := c.callJSON(ctx, k)
	if err != nil {
		return nil, err
	}
	return decodeConversion(data)
}

// Wait waits until the conversion is done and returns it.
//
// It fails with a [*ConversionFailedError] when the conversion fails, expires or is canceled,
// with a [*ConfirmationRequiredError] when it waits for a go-ahead (see [Client.Confirm]), and
// with a [*ConversionTimeoutError] when the wait timeout ([WithWaitTimeout], 10 minutes by
// default) passes or ctx ends first; the conversion may still finish then.
//
// The API holds each status request until something changes (up to 25 seconds), so a long
// conversion costs a request every 25 seconds. Answers that come back early are followed by
// a pause of 1 second, doubling up to 10.
func (c *Client) Wait(ctx context.Context, id string) (*Conversion, error) {
	current, err := c.GetConversion(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.wait(ctx, current)
}

func (c *Client) wait(ctx context.Context, current *Conversion) (*Conversion, error) {
	waitCtx := ctx
	if c.waitTimeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, c.waitTimeout)
		defer cancel()
	}
	timedOut := func(err error) error {
		if ctx.Err() == nil && waitCtx.Err() != nil {
			return &ConversionTimeoutError{Conversion: current, Timeout: c.waitTimeout, Err: waitCtx.Err()}
		}
		if ctx.Err() != nil {
			return &ConversionTimeoutError{Conversion: current, Err: ctx.Err()}
		}
		return err
	}
	var pause time.Duration
	for !current.Status.settled() {
		seconds := longPollSeconds
		if deadline, ok := waitCtx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return nil, timedOut(waitCtx.Err())
			}
			seconds = max(1, min(longPollSeconds, int(remaining/time.Second)))
		}
		started := time.Now()
		next, err := c.getConversion(waitCtx, current.ID, seconds)
		if err != nil {
			return nil, timedOut(err)
		}
		current = next
		if current.Status.settled() {
			break
		}
		if time.Since(started) < time.Duration(seconds)*time.Second/2 {
			pause = pollBackoff(pause)
			if err := c.sleep(waitCtx, pause); err != nil {
				return nil, timedOut(err)
			}
		} else {
			pause = 0
		}
	}
	if err := settledError(current); err != nil {
		return nil, err
	}
	return current, nil
}

// pollBackoff is the next pause between status requests that came back early: 1 s, doubling
// to 10 s.
func pollBackoff(previous time.Duration) time.Duration {
	if previous <= 0 {
		return time.Second
	}
	return min(previous*2, 10*time.Second)
}

// ConvertFile uploads a local file, converts it to the format to ("jpg", "mp4", "pdf"...)
// and, unless p.NoWait, waits until it is done (see [Client.Wait]). Save the result with
// [Client.DownloadFile] or [Client.Download]. p may be nil.
func (c *Client) ConvertFile(ctx context.Context, path, to string, p *ConvertParams) (*Conversion, error) {
	if to == "" {
		return nil, errNoTarget
	}
	upload, err := c.UploadFile(ctx, path, uploadOptions(p))
	if err != nil {
		return nil, err
	}
	return c.convert(ctx, ConversionParams{To: to, UploadID: upload.ID}, p)
}

// ConvertReader is [Client.ConvertFile] for what r reads, as a file named fileName (see
// [Client.UploadReader]).
func (c *Client) ConvertReader(ctx context.Context, r io.Reader, fileName, to string, p *ConvertParams) (*Conversion, error) {
	if to == "" {
		return nil, errNoTarget
	}
	upload, err := c.UploadReader(ctx, r, fileName, uploadOptions(p))
	if err != nil {
		return nil, err
	}
	return c.convert(ctx, ConversionParams{To: to, UploadID: upload.ID}, p)
}

// ConvertURL converts the file behind a link (a direct link, or a Google Drive, Dropbox or
// OneDrive share link): Converter Studio fetches it itself, so it never passes through this
// machine. Otherwise it is [Client.ConvertFile].
func (c *Client) ConvertURL(ctx context.Context, link, to string, p *ConvertParams) (*Conversion, error) {
	return c.convert(ctx, ConversionParams{To: to, URL: link}, p)
}

func (c *Client) convert(ctx context.Context, base ConversionParams, p *ConvertParams) (*Conversion, error) {
	if p != nil {
		base.Options, base.MaxCredits, base.QuoteOnly = p.Options, p.MaxCredits, p.QuoteOnly
		base.CallbackURL, base.IdempotencyKey = p.CallbackURL, p.IdempotencyKey
	}
	created, err := c.CreateConversion(ctx, base)
	if err != nil || (p != nil && p.NoWait) {
		return created, err
	}
	return c.wait(ctx, created)
}

func uploadOptions(p *ConvertParams) *UploadOptions {
	if p == nil {
		return nil
	}
	return &UploadOptions{PartSize: p.PartSize}
}

var errNoTarget = errors.New("converterstudio: the format to convert to is needed (such as \"jpg\")")

func conversionBody(p ConversionParams) (map[string]any, error) {
	to := strings.ToLower(strings.TrimLeft(strings.TrimSpace(p.To), "."))
	if to == "" {
		return nil, errNoTarget
	}
	if (p.URL == "") == (p.UploadID == "") {
		return nil, errors.New("converterstudio: give either a URL or an upload ID")
	}
	if p.MaxCredits < 0 {
		return nil, errors.New("converterstudio: MaxCredits must be 0 or more")
	}
	if p.QuoteOnly && p.MaxCredits > 0 {
		return nil, errors.New("converterstudio: QuoteOnly and MaxCredits cannot be used together")
	}
	body := map[string]any{"to": to}
	if p.URL != "" {
		body["url"] = p.URL
	} else {
		body["uploadId"] = p.UploadID
	}
	if len(p.Options) > 0 {
		body["options"] = p.Options
	}
	switch {
	case p.QuoteOnly:
		body["maxCredits"] = 0
	case p.MaxCredits > 0:
		body["maxCredits"] = p.MaxCredits
	}
	if p.CallbackURL != "" {
		body["callbackUrl"] = p.CallbackURL
	}
	return body, nil
}

func conversionPath(id string) (string, error) {
	if id == "" {
		return "", errors.New("converterstudio: a conversion ID is needed")
	}
	return "/v1/conversions/" + url.PathEscape(id), nil
}

func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "cvs-go-" + hex.EncodeToString(b)
}

func validIdempotencyKey(key string) bool {
	if len(key) < 1 || len(key) > 255 {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return false
		}
	}
	return true
}
