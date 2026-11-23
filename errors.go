package converterstudio

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Errors to test with [errors.Is]. An [*APIError] matches the one for its HTTP status, so
// errors.Is(err, ErrInsufficientCredits) is true for a 402 answer; [errors.As] gives the
// APIError itself, with the API's code and message.
var (
	// ErrMissingAPIKey: no API key was given and CONVERTER_STUDIO_API_KEY is not set.
	ErrMissingAPIKey = errors.New("converterstudio: no API key: use WithAPIKey or set " +
		APIKeyEnv + " (create a key at https://converter.studio/account#api-keys)")
	// ErrBadRequest (400): the request was not right ("invalidRequest", "invalidUrl",
	// "invalidCallbackUrl", "invalidIdempotencyKey"...).
	ErrBadRequest = errors.New("converterstudio: bad request")
	// ErrAuthentication (401): the API key does not work (wrong or deleted).
	ErrAuthentication = errors.New("converterstudio: the API key does not work")
	// ErrInsufficientCredits (402): not enough credits (Params: "needed", "balance").
	ErrInsufficientCredits = errors.New("converterstudio: not enough credits")
	// ErrPermissionDenied (403): the plan does not include this.
	ErrPermissionDenied = errors.New("converterstudio: not included in the plan")
	// ErrNotFound (404): no such conversion or upload on this account.
	ErrNotFound = errors.New("converterstudio: not found")
	// ErrConflict (409): the request conflicts with what is there ("idempotencyKeyReused", an
	// upload that has not received its file yet...).
	ErrConflict = errors.New("converterstudio: conflict")
	// ErrGone (410): the result has been deleted ("fileExpired").
	ErrGone = errors.New("converterstudio: the result has been deleted")
	// ErrFileTooLarge (413): the file is larger than the plan allows (Params: "maxSize").
	ErrFileTooLarge = errors.New("converterstudio: the file is too large")
	// ErrUnprocessable (422): the file cannot be converted this way ("unsupportedConversion").
	ErrUnprocessable = errors.New("converterstudio: cannot be converted this way")
	// ErrRateLimited (429): too many requests; retried on their own first.
	ErrRateLimited = errors.New("converterstudio: too many requests")
	// ErrServer (5xx): the service is busy or failed ("serverBusy"); retried on their own
	// first.
	ErrServer = errors.New("converterstudio: server error")
	// ErrNotDone: a download was asked for a conversion that is not done.
	ErrNotDone = errors.New("converterstudio: the conversion is not done yet; wait for it first")
)

var statusErrors = map[int]error{
	http.StatusBadRequest:            ErrBadRequest,
	http.StatusUnauthorized:          ErrAuthentication,
	http.StatusPaymentRequired:       ErrInsufficientCredits,
	http.StatusForbidden:             ErrPermissionDenied,
	http.StatusNotFound:              ErrNotFound,
	http.StatusConflict:              ErrConflict,
	http.StatusGone:                  ErrGone,
	http.StatusRequestEntityTooLarge: ErrFileTooLarge,
	http.StatusUnprocessableEntity:   ErrUnprocessable,
	http.StatusTooManyRequests:       ErrRateLimited,
}

// APIError is an error answer of the API.
type APIError struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Code is the API's stable error code ("invalidApiKey", "notEnoughCredits",
	// "unsupportedConversion"...). It is empty only when the answer did not come from the API
	// itself (a proxy's error page).
	Code string
	// Message is the API's English sentence for people.
	Message string
	// Params holds the values in the message (numbers are float64).
	Params map[string]any
	// RetryAfter is how long the API asked to wait before trying again (429, 503), or 0.
	RetryAfter time.Duration
	// Header is the answer's header.
	Header http.Header
}

// Error returns the API's message, a sentence meant to be shown to people.
func (e *APIError) Error() string { return e.Message }

// Is reports whether target is the sentinel error for e's HTTP status, such as
// [ErrAuthentication] for 401 and [ErrServer] for any 5xx.
func (e *APIError) Is(target error) bool {
	if e.StatusCode >= 500 {
		return target == ErrServer
	}
	sentinel, ok := statusErrors[e.StatusCode]
	return ok && target == sentinel
}

// The site's English messages for the codes the download route sends without one.
var fallbackMessages = map[string]string{
	"fileExpired":     "This file has already been deleted. Please convert it again.",
	"notFound":        "We couldn't find that conversion.",
	"tooManyRequests": "You're going a bit fast. Please wait a moment and try again.",
	"serverBusy":      "Our servers are busy right now. Please try again in a few minutes.",
	"invalidRequest":  "Something about that request wasn't right.",
	"invalidApiKey":   "This API key does not work. Create one on your account page at converter.studio.",
}

// errorFromResponse reads (at most 64 KB of) an error answer and closes its body. The API
// sends {"error": {"code", "message", "params"}}; the download route sends {"error": "code"}.
func errorFromResponse(resp *http.Response) *APIError {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	e := &APIError{StatusCode: resp.StatusCode, Header: resp.Header}
	var body struct {
		Error  json.RawMessage `json:"error"`
		Params map[string]any  `json:"params"`
	}
	if json.Unmarshal(data, &body) == nil && len(body.Error) > 0 {
		var info ErrorInfo
		var code string
		if json.Unmarshal(body.Error, &info) == nil {
			e.Code, e.Message, e.Params = info.Code, info.Message, info.Params
		} else if json.Unmarshal(body.Error, &code) == nil {
			e.Code, e.Params = code, body.Params
		}
	}
	if e.Message == "" {
		e.Message = fallbackMessages[e.Code]
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(fmt.Sprintf("The Converter Studio API answered %d %s",
			resp.StatusCode, http.StatusText(resp.StatusCode))) + "."
	}
	e.RetryAfter = retryAfter(resp.Header, time.Now())
	return e
}

// retryAfter is the Retry-After header in seconds or as an HTTP date, or 0.
func retryAfter(h http.Header, now time.Time) time.Duration {
	value := strings.TrimSpace(h.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds * float64(time.Second))
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}

// ConnectionError means the API (or an upload or download link) could not be reached, or the
// connection broke, after the retries. It names only the host: links carry tokens.
type ConnectionError struct {
	// Host is the host that could not be reached.
	Host string
	// Err is the network error.
	Err     error
	timeout bool
}

func (e *ConnectionError) Error() string {
	if e.timeout {
		return fmt.Sprintf("converterstudio: the request to %s timed out", e.Host)
	}
	return fmt.Sprintf("converterstudio: could not reach %s: %v", e.Host, e.Err)
}

func (e *ConnectionError) Unwrap() error { return e.Err }

// Timeout reports whether the request went without progress for longer than the client's
// timeout ([WithTimeout]).
func (e *ConnectionError) Timeout() bool { return e.timeout }

// ConversionFailedError means a conversion failed, expired or was canceled. Nothing is charged
// for a failure.
type ConversionFailedError struct {
	// Code is the API's ("conversionFailed", "noText", "wrongPassword", "remoteNotFound"...),
	// or "fileExpired" and "canceled".
	Code string
	// Message is a sentence for people.
	Message string
	// Params holds the values in the message (numbers are float64).
	Params map[string]any
	// Conversion is its last known state.
	Conversion *Conversion
}

func newConversionFailed(c *Conversion) *ConversionFailedError {
	e := &ConversionFailedError{Conversion: c}
	switch {
	case c.Error != nil && c.Error.Code != "":
		e.Code, e.Message, e.Params = c.Error.Code, c.Error.Message, c.Error.Params
		if e.Message == "" {
			e.Message = c.Error.Code
		}
	case c.Status == StatusExpired:
		e.Code, e.Message = "fileExpired", "The result was deleted after its time ran out."
	case c.Status == StatusCanceled:
		e.Code, e.Message = "canceled", "The conversion was canceled; nothing was charged."
	default:
		e.Code, e.Message = "conversionFailed", c.Next
		if e.Message == "" {
			e.Message = "The conversion failed."
		}
	}
	return e
}

// Error returns the API's message, a sentence meant to be shown to people.
func (e *ConversionFailedError) Error() string { return e.Message }

// ConfirmationRequiredError means a conversion costs more than its maximum (the MaxCredits of
// [ConvertParams] and [ConversionParams], 20 by default): it waits for [Client.Confirm] and
// nothing has been charged.
type ConfirmationRequiredError struct {
	// Credits is the price, or -1 if the API did not say.
	Credits int
	// MaxCredits is the price that would have gone ahead on its own (0 with QuoteOnly).
	MaxCredits int
	// Conversion is the conversion that waits (StatusQuoted).
	Conversion *Conversion
}

func newConfirmationRequired(c *Conversion) *ConfirmationRequiredError {
	e := &ConfirmationRequiredError{Credits: -1, Conversion: c}
	if c.Credits != nil {
		e.Credits = *c.Credits
	}
	if c.MaxCredits != nil {
		e.MaxCredits = *c.MaxCredits
	}
	return e
}

func (e *ConfirmationRequiredError) Error() string {
	if e.Credits < 0 {
		return "This conversion waits for your go-ahead. Confirm it to start, or leave it: " +
			"nothing has been charged."
	}
	return fmt.Sprintf("This conversion costs %d %s, more than the %d allowed to go ahead on "+
		"their own. Confirm it to start, or leave it: nothing has been charged.",
		e.Credits, plural(e.Credits, "credit", "credits"), e.MaxCredits)
}

// ConversionTimeoutError means a conversion did not finish while [Client.Wait] (or a Convert
// method) waited for it: the wait timeout ([WithWaitTimeout]) passed or the context ended. It
// may still finish: look it up later by its ID with [Client.GetConversion].
type ConversionTimeoutError struct {
	// Conversion is its last known state.
	Conversion *Conversion
	// Timeout is the wait timeout that passed, or 0 when the context ended first.
	Timeout time.Duration
	// Err is the context's error: [context.DeadlineExceeded] or [context.Canceled].
	Err error
}

func (e *ConversionTimeoutError) Error() string {
	c := e.Conversion
	if e.Timeout > 0 {
		return fmt.Sprintf("The conversion did not finish within %s (it is %s). It may still "+
			"finish: look it up later by its id, %s.", e.Timeout, c.Status, c.ID)
	}
	return fmt.Sprintf("Stopped waiting for the conversion (it is %s): %v. It may still finish: "+
		"look it up later by its id, %s.", c.Status, e.Err, c.ID)
}

func (e *ConversionTimeoutError) Unwrap() error { return e.Err }

// settledError is nil for a done conversion, and the error for one that ended without a
// result or waits for a go-ahead.
func settledError(c *Conversion) error {
	switch {
	case c.Status == StatusDone:
		return nil
	case c.Status == StatusQuoted:
		return newConfirmationRequired(c)
	case c.Status.Finished():
		return newConversionFailed(c)
	}
	return fmt.Errorf("%w (it is %s)", ErrNotDone, c.Status)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
