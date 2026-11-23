package converterstudio_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	converterstudio "github.com/tariksucu/converter-studio-go"
)

func TestErrorsByStatus(t *testing.T) {
	for status, sentinel := range map[int]error{
		400: converterstudio.ErrBadRequest, 401: converterstudio.ErrAuthentication,
		402: converterstudio.ErrInsufficientCredits, 403: converterstudio.ErrPermissionDenied,
		404: converterstudio.ErrNotFound, 409: converterstudio.ErrConflict, 410: converterstudio.ErrGone,
		413: converterstudio.ErrFileTooLarge, 422: converterstudio.ErrUnprocessable,
		429: converterstudio.ErrRateLimited, 500: converterstudio.ErrServer, 503: converterstudio.ErrServer,
	} {
		api := newFakeAPI(t)
		client, _ := api.client(t, converterstudio.WithMaxRetries(0))
		api.push("/api/v1/account", apiError(status, "someCode", "Some sentence."))
		_, err := client.Account(ctx)
		if !errors.Is(err, sentinel) || err.Error() != "Some sentence." {
			t.Errorf("%d: %v", status, err)
		}
		if status != 404 && errors.Is(err, converterstudio.ErrNotFound) {
			t.Errorf("%d matches ErrNotFound", status)
		}
		wrapped := fmt.Errorf("converting: %w", err)
		var apiErr *converterstudio.APIError
		if !errors.As(wrapped, &apiErr) || apiErr.StatusCode != status || apiErr.Code != "someCode" {
			t.Errorf("%d: %#v", status, apiErr)
		}
	}
	other := &converterstudio.APIError{StatusCode: 418, Message: "Teapot."}
	if errors.Is(other, converterstudio.ErrBadRequest) || errors.Is(other, converterstudio.ErrServer) {
		t.Fatal("418 matches a sentinel")
	}
}

func TestAnErrorWithParams(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	api.push("/api/v1/conversions", respond(402,
		`{"error":{"code":"notEnoughCredits","message":"This needs 7 credits; you have 2.","params":{"needed":7,"balance":2}}}`))
	_, err := client.CreateConversion(ctx, converterstudio.ConversionParams{To: "mp4", URL: "https://example.com/a.mkv"})
	var apiErr *converterstudio.APIError
	if !errors.As(err, &apiErr) || !errors.Is(err, converterstudio.ErrInsufficientCredits) {
		t.Fatalf("got %v", err)
	}
	if apiErr.Params["needed"] != float64(7) || apiErr.Params["balance"] != float64(2) || apiErr.Header.Get("Content-Type") == "" {
		t.Fatalf("params %v", apiErr.Params)
	}
}

func TestSiteStyleAndForeignErrors(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t, converterstudio.WithMaxRetries(0))
	api.push("/api/v1/account",
		respond(429, `{"error":"tooManyRequests","params":{"seconds":3}}`, "Retry-After", "3"),
		respond(502, `<html><body>Bad gateway</body></html>`),
		respond(418, `{"error":{"code":"teapot"}}`),
		respond(301, ``, "Location", "https://elsewhere.example/api/v1/account"),
	)
	_, err := client.Account(ctx)
	var apiErr *converterstudio.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "tooManyRequests" || apiErr.RetryAfter != 3*time.Second ||
		apiErr.Params["seconds"] != float64(3) || !strings.HasPrefix(err.Error(), "You're going a bit fast.") {
		t.Fatalf("got %#v", err)
	}
	_, err = client.Account(ctx)
	if !errors.As(err, &apiErr) || apiErr.Code != "" || err.Error() != "The Converter Studio API answered 502 Bad Gateway." {
		t.Fatalf("got %v", err)
	}
	_, err = client.Account(ctx)
	if !errors.As(err, &apiErr) || apiErr.Code != "teapot" || err.Error() != "The Converter Studio API answered 418 I'm a teapot." {
		t.Fatalf("got %v", err)
	}
	// The API's own requests do not follow redirects (the key stays with the API).
	_, err = client.Account(ctx)
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 301 {
		t.Fatalf("got %v", err)
	}
	if n := len(api.of("GET", "/api/v1/account")); n != 4 {
		t.Fatalf("%d requests", n)
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for value, want := range map[string]time.Duration{
		"": 0, "5": 5 * time.Second, "1.5": 1500 * time.Millisecond, "-3": 0, "soon": 0,
		now.Add(30 * time.Second).Format(http.TimeFormat):  30 * time.Second,
		now.Add(-30 * time.Second).Format(http.TimeFormat): 0,
	} {
		h := http.Header{}
		if value != "" {
			h.Set("Retry-After", value)
		}
		if got := converterstudio.RetryAfter(h, now); got != want {
			t.Errorf("%q: %s", value, got)
		}
	}
}

func TestRetryDelayAndPollBackoff(t *testing.T) {
	if got := converterstudio.RetryDelay(0, 90*time.Second); got != time.Minute {
		t.Fatalf("Retry-After is capped at a minute: %s", got)
	}
	if got := converterstudio.RetryDelay(3, 2*time.Second); got != 2*time.Second {
		t.Fatalf("Retry-After: %s", got)
	}
	for attempt, base := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second, 8 * time.Second} {
		for range 20 {
			if got := converterstudio.RetryDelay(attempt, 0); got < base/2 || got >= base {
				t.Fatalf("attempt %d: %s", attempt, got)
			}
		}
	}
	steps := []time.Duration{0}
	for range 6 {
		steps = append(steps, converterstudio.PollBackoff(steps[len(steps)-1]))
	}
	want := []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("back-off %v", steps)
		}
	}
}

func TestPartRanges(t *testing.T) {
	cases := map[[2]int64]string{
		{0, 10}:   "[[0 0]]",
		{10, 10}:  "[[0 10]]",
		{25, 10}:  "[[0 10] [10 10] [20 5]]",
		{20, 10}:  "[[0 10] [10 10]]",
		{11, 100}: "[[0 11]]",
	}
	for in, want := range cases {
		if got := fmt.Sprint(converterstudio.PartRanges(in[0], in[1])); got != want {
			t.Errorf("%v: %s", in, got)
		}
	}
}

func TestConversionErrorsWithoutDetails(t *testing.T) {
	quoted := &converterstudio.ConfirmationRequiredError{Credits: -1, Conversion: &converterstudio.Conversion{}}
	if !strings.HasPrefix(quoted.Error(), "This conversion waits for your go-ahead.") {
		t.Fatalf("%q", quoted.Error())
	}
	late := &converterstudio.ConversionTimeoutError{
		Conversion: &converterstudio.Conversion{ID: "abc", Status: converterstudio.StatusQueued}, Timeout: time.Minute,
	}
	if late.Error() != "The conversion did not finish within 1m0s (it is queued). It may still finish: look it up later by its id, abc." {
		t.Fatalf("%q", late.Error())
	}
	conn := &converterstudio.ConnectionError{Host: "converter.studio", Err: errors.New("refused")}
	if conn.Error() != "converterstudio: could not reach converter.studio: refused" || conn.Timeout() {
		t.Fatalf("%q", conn.Error())
	}
}
