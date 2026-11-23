package converterstudio_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	converterstudio "github.com/tariksucu/converter-studio-go"
)

func TestAFailedConversion(t *testing.T) {
	api := newFakeAPI(t)
	api.statuses = []string{"processing", "failed"}
	api.failure = map[string]any{
		"code": "wrongPassword", "message": "The password for this archive is wrong.",
		"params": map[string]any{"tries": 1},
	}
	client, _ := api.client(t)
	_, err := client.ConvertURL(ctx, "https://example.com/locked.zip", "tar", nil)
	var failed *converterstudio.ConversionFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("got %v", err)
	}
	if failed.Code != "wrongPassword" || err.Error() != "The password for this archive is wrong." ||
		failed.Params["tries"] != float64(1) || failed.Conversion.Status != converterstudio.StatusFailed {
		t.Fatalf("failure: %+v", failed)
	}
	if _, err := client.Download(ctx, failed.Conversion, &strings.Builder{}); !errors.As(err, &failed) {
		t.Fatalf("downloading a failed conversion: %v", err)
	}
	// A failure without its reason still says something.
	api.failure = nil
	_, err = client.ConvertURL(ctx, "https://example.com/x.zip", "tar", nil)
	if !errors.As(err, &failed) || failed.Code != "conversionFailed" || failed.Message != "Check again." {
		t.Fatalf("got %+v", failed)
	}
}

func TestExpiredAndCanceled(t *testing.T) {
	for status, code := range map[string]string{"expired": "fileExpired", "canceled": "canceled"} {
		api := newFakeAPI(t)
		api.statuses = []string{status}
		client, _ := api.client(t)
		_, err := client.ConvertURL(ctx, "https://example.com/a.heic", "jpg", nil)
		var failed *converterstudio.ConversionFailedError
		if !errors.As(err, &failed) || failed.Code != code || failed.Message == "" {
			t.Fatalf("%s: %v", status, err)
		}
	}
}

func TestAPriceOverTheLimitWaitsForAGoAhead(t *testing.T) {
	api := newFakeAPI(t)
	api.credits = func(name string) int {
		if strings.HasSuffix(name, ".mkv") {
			return 7
		}
		return 1
	}
	client, _ := api.client(t)
	_, err := client.ConvertURL(ctx, "https://example.com/lecture.mkv", "mp4", &converterstudio.ConvertParams{MaxCredits: 5})
	var quoted *converterstudio.ConfirmationRequiredError
	if !errors.As(err, &quoted) || quoted.Credits != 7 || quoted.MaxCredits != 5 || !quoted.Conversion.NeedsConfirmation() {
		t.Fatalf("got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "This conversion costs 7 credits, more than the 5 allowed") {
		t.Fatalf("message %q", err)
	}
	confirmed, err := client.Confirm(ctx, quoted.Conversion.ID)
	if err != nil || confirmed.Status != converterstudio.StatusQueued {
		t.Fatalf("%v %v", confirmed, err)
	}
	done, err := client.Wait(ctx, confirmed.ID)
	if err != nil || !done.Done() {
		t.Fatalf("%v %v", done, err)
	}
	if got := api.of("POST", "/api/v1/conversions/"+confirmed.ID+"/confirm"); len(got) != 1 {
		t.Fatal("no confirm request")
	}
	// QuoteOnly sends a maximum of 0 credits.
	_, err = client.ConvertURL(ctx, "https://example.com/photo.heic", "jpg", &converterstudio.ConvertParams{QuoteOnly: true})
	if !errors.As(err, &quoted) || quoted.Credits != 1 || quoted.MaxCredits != 0 {
		t.Fatalf("got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "This conversion costs 1 credit, more than the 0") {
		t.Fatalf("message %q", err)
	}
	posts := api.of("POST", "/api/v1/conversions")
	if !strings.Contains(string(posts[len(posts)-1].Body), `"maxCredits":0`) {
		t.Fatalf("body %s", posts[len(posts)-1].Body)
	}
}

func TestWaitingTimesOut(t *testing.T) {
	api := newFakeAPI(t)
	api.statuses = []string{"processing"}
	client, pauses := api.client(t, converterstudio.WithWaitTimeout(300*time.Millisecond))
	// The pauses really wait here (up to the deadline).
	converterstudio.SetSleep(client, func(ctx context.Context, d time.Duration) error {
		*pauses = append(*pauses, d)
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	created, _ := client.ConvertURL(ctx, "https://example.com/a.heic", "jpg", &converterstudio.ConvertParams{NoWait: true})
	_, err := client.Wait(ctx, created.ID)
	var late *converterstudio.ConversionTimeoutError
	if !errors.As(err, &late) || late.Conversion.ID != created.ID || late.Timeout != 300*time.Millisecond {
		t.Fatalf("got %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "did not finish within 300ms (it is processing)") ||
		!strings.Contains(err.Error(), created.ID) {
		t.Fatalf("message %q", err)
	}
	gets := api.of("GET", "/api/v1/conversions/"+created.ID)
	if gets[1].Query.Get("wait") != "1" {
		t.Fatalf("a long poll longer than the time left: %v", gets[1].Query)
	}
	// The caller's own context.
	client, _ = api.client(t, converterstudio.WithWaitTimeout(0))
	stop, cancel := context.WithCancel(ctx)
	cancel()
	_, err = client.Wait(stop, created.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	api.holdWaits = time.Second
	_, err = client.Wait(short, created.ID)
	if !errors.As(err, &late) || late.Timeout != 0 || !errors.Is(err, context.DeadlineExceeded) ||
		!strings.HasPrefix(err.Error(), "Stopped waiting for the conversion (it is processing)") {
		t.Fatalf("got %v", err)
	}
}

func TestPausesGrowWhileAnswersComeBackEarly(t *testing.T) {
	api := newFakeAPI(t)
	api.statuses = []string{"queued", "processing", "processing", "processing", "processing", "processing", "done"}
	client, pauses := api.client(t)
	if _, err := client.ConvertURL(ctx, "https://example.com/a.heic", "jpg", nil); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	if len(*pauses) != len(want) {
		t.Fatalf("pauses %v", *pauses)
	}
	for i := range want {
		if (*pauses)[i] != want[i] {
			t.Fatalf("pauses %v", *pauses)
		}
	}
}

func TestLongPollsThatTakeTheirTimeNeedNoPause(t *testing.T) {
	api := newFakeAPI(t)
	api.statuses = []string{"processing", "processing", "done"}
	api.holdWaits = 600 * time.Millisecond
	// With 2 seconds left, each status request may be held for 1 second; held 0.6 s, it is
	// not early.
	client, pauses := api.client(t, converterstudio.WithWaitTimeout(2*time.Second))
	created, _ := client.ConvertURL(ctx, "https://example.com/a.heic", "jpg", &converterstudio.ConvertParams{NoWait: true})
	if _, err := client.Wait(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if len(*pauses) != 0 {
		t.Fatalf("pauses %v", *pauses)
	}
}
