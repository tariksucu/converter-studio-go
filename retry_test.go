package converterstudio_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	converterstudio "github.com/tariksucu/converter-studio-go"
)

// noReuse is an HTTP client that opens a new connection for every request, so that the
// transport never retries on its own and every attempt reaches the fake API.
func noReuse() converterstudio.Option {
	return converterstudio.WithHTTPClient(&http.Client{Transport: &http.Transport{DisableKeepAlives: true}})
}

func TestLargeFilesGoInParts(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	data := bytes.Repeat([]byte("0123456789"), 25) // 250 bytes
	upload, err := client.UploadFile(ctx, writeFile(t, "song.wav", string(data)), &converterstudio.UploadOptions{PartSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	puts := api.of("PUT", "/api/v1/uploads/"+upload.ID)
	if len(puts) != 3 {
		t.Fatalf("%d parts", len(puts))
	}
	for i, want := range []string{"0", "100", "200"} {
		if got := puts[i].Header.Get("Upload-Offset"); got != want {
			t.Errorf("part %d: offset %q", i, got)
		}
	}
	if len(puts[2].Body) != 50 {
		t.Fatalf("last part: %d bytes", len(puts[2].Body))
	}
	conversion, err := client.CreateConversion(ctx, converterstudio.ConversionParams{To: "mp3", UploadID: upload.ID})
	if err != nil || conversion.InputSize != 250 {
		t.Fatalf("%v %v", conversion, err)
	}
	if !strings.Contains(upload.String(), upload.ID) || strings.Contains(upload.String(), "token") {
		t.Fatalf("String() %q", upload.String())
	}
}

func TestAReaderThatCannotSeekIsReadFirst(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	small := io.MultiReader(strings.NewReader("abc"), strings.NewReader("def"))
	upload, err := client.UploadReader(ctx, small, "small.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(api.of("PUT", "/api/v1/uploads/"+upload.ID)[0].Body); got != "abcdef" {
		t.Fatalf("sent %q", got)
	}
	// Over 8 MB: through a temporary file, in parts that can be sent again.
	big := bytes.Repeat([]byte{7}, 8<<20+5)
	api.push("/api/v1/uploads/up00000002", respond(503, `{"error":{"code":"serverBusy","message":"Busy."}}`))
	upload, err = client.UploadReader(ctx, io.MultiReader(bytes.NewReader(big)), "big.bin", &converterstudio.UploadOptions{PartSize: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var sent []byte
	puts := api.of("PUT", "/api/v1/uploads/"+upload.ID)
	if len(puts) != 4 {
		t.Fatalf("%d attempts", len(puts))
	}
	for _, p := range puts[1:] {
		sent = append(sent, p.Body...)
	}
	if !bytes.Equal(sent, big) {
		t.Fatalf("sent %d bytes, not the file", len(sent))
	}
	// A seekable reader is sent from where it stands.
	r := strings.NewReader("skip:keep")
	_, _ = r.Seek(5, io.SeekStart)
	upload, err = client.UploadReader(ctx, r, "keep.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(api.of("PUT", "/api/v1/uploads/"+upload.ID)[0].Body); got != "keep" {
		t.Fatalf("sent %q", got)
	}
	// An empty file is one empty part.
	upload, err = client.UploadReader(ctx, strings.NewReader(""), "empty.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if put := api.of("PUT", "/api/v1/uploads/"+upload.ID)[0]; len(put.Body) != 0 || put.Header.Get("Content-Length") != "0" {
		t.Fatalf("empty upload: %v", put.Header)
	}
}

func TestAPartIsSentAgainAfterAFailure(t *testing.T) {
	api := newFakeAPI(t)
	client, pauses := api.client(t, noReuse())
	path := "/api/v1/uploads/up00000001"
	api.push(path, respond(503, `{"error":{"code":"serverBusy","message":"Busy."}}`, "Retry-After", "2"), hangUp())
	upload, err := client.UploadFile(ctx, writeFile(t, "a.txt", "0123456789"), &converterstudio.UploadOptions{PartSize: 6})
	if err != nil {
		t.Fatal(err)
	}
	puts := api.of("PUT", path)
	if len(puts) != 4 || string(puts[2].Body) != "012345" || string(puts[3].Body) != "6789" {
		t.Fatalf("%d attempts", len(puts))
	}
	if (*pauses)[0] != 2*time.Second {
		t.Fatalf("Retry-After was not followed: %v", *pauses)
	}
	if _, err := client.CreateConversion(ctx, converterstudio.ConversionParams{To: "pdf", UploadID: upload.ID}); err != nil {
		t.Fatal(err)
	}
}

func TestALastPartWhoseAnswerWasLostCountsAsSent(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t, noReuse())
	path := "/api/v1/uploads/up00000001"
	// The part arrives, the answer is lost, and the slot is gone when it is sent again.
	api.push(path, loseAnswer(), apiError(404, "notFound", "We couldn't find that conversion."))
	if _, err := client.UploadFile(ctx, writeFile(t, "a.txt", "abc"), nil); err != nil {
		t.Fatal(err)
	}
	// A 404 on the first attempt is an error.
	api.push("/api/v1/uploads/up00000002", apiError(404, "notFound", "We couldn't find that conversion."))
	if _, err := client.UploadFile(ctx, writeFile(t, "b.txt", "abc"), nil); !errors.Is(err, converterstudio.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestNetworkErrorsAndBusyAnswersAreRetried(t *testing.T) {
	api := newFakeAPI(t)
	client, pauses := api.client(t, noReuse())
	api.push("/api/v1/account", hangUp(), respond(502, "<html>Bad gateway</html>"))
	if _, err := client.Account(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(api.of("GET", "/api/v1/account")); n != 3 {
		t.Fatalf("%d attempts", n)
	}
	if len(*pauses) != 2 || (*pauses)[0] < 250*time.Millisecond || (*pauses)[0] > 500*time.Millisecond ||
		(*pauses)[1] < 500*time.Millisecond || (*pauses)[1] > time.Second {
		t.Fatalf("pauses %v", *pauses)
	}
	api.push("/api/v1/account", respond(429, `{"error":{"code":"tooManyRequests","message":"Slow down."}}`, "Retry-After", "7"))
	if _, err := client.Account(ctx); err != nil {
		t.Fatal(err)
	}
	if (*pauses)[2] != 7*time.Second {
		t.Fatalf("Retry-After: %v", *pauses)
	}
}

func TestARetriedConversionKeepsItsIdempotencyKey(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t, noReuse())
	api.push("/api/v1/conversions", loseAnswer(), respond(503, `{"error":{"code":"serverBusy","message":"Busy."}}`))
	conversion, err := client.CreateConversion(ctx, converterstudio.ConversionParams{To: "jpg", URL: "https://example.com/a.heic"})
	if err != nil {
		t.Fatal(err)
	}
	posts := api.of("POST", "/api/v1/conversions")
	if len(posts) != 3 {
		t.Fatalf("%d attempts", len(posts))
	}
	key := posts[0].Header.Get("Idempotency-Key")
	for _, p := range posts {
		if p.Header.Get("Idempotency-Key") != key || !bytes.Equal(p.Body, posts[0].Body) {
			t.Fatal("a retry changed the request")
		}
	}
	// The first attempt made the conversion; the retry got it back.
	if !conversion.Replayed || len(api.jobs) != 1 {
		t.Fatalf("replayed %v, %d jobs", conversion.Replayed, len(api.jobs))
	}
}

func TestRetriesRunOut(t *testing.T) {
	api := newFakeAPI(t)
	client, pauses := api.client(t, converterstudio.WithMaxRetries(1))
	busy := respond(503, `{"error":{"code":"serverBusy","message":"Our servers are busy right now."}}`)
	api.push("/api/v1/account", busy, busy)
	_, err := client.Account(ctx)
	var apiErr *converterstudio.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "serverBusy" || !errors.Is(err, converterstudio.ErrServer) {
		t.Fatalf("got %v", err)
	}
	if len(api.of("GET", "/api/v1/account")) != 2 || len(*pauses) != 1 {
		t.Fatal("not one retry")
	}
	client, _ = api.client(t, converterstudio.WithMaxRetries(0), noReuse())
	api.push("/api/v1/account", hangUp())
	_, err = client.Account(ctx)
	var connErr *converterstudio.ConnectionError
	if !errors.As(err, &connErr) || connErr.Timeout() {
		t.Fatalf("got %v", err)
	}
}

func TestConnectionErrorsNameOnlyTheHost(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t, converterstudio.WithMaxRetries(0), noReuse())
	created, _ := client.ConvertURL(ctx, "https://example.com/a.heic", "jpg", nil)
	api.push("/api/jobs/"+created.ID+"/download", hangUp())
	_, err := client.Download(ctx, created, io.Discard)
	var connErr *converterstudio.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "/jobs/") {
		t.Fatalf("the link is in the error: %v", err)
	}
	if !strings.Contains(err.Error(), connErr.Host) || connErr.Unwrap() == nil {
		t.Fatalf("no host in %v", err)
	}
	// Nothing listens there.
	api.server.Close()
	_, err = client.Account(ctx)
	if !errors.As(err, &connErr) || !strings.Contains(err.Error(), "could not reach 127.0.0.1:") {
		t.Fatalf("got %v", err)
	}
}

func TestARequestWithoutProgressTimesOut(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t, converterstudio.WithTimeout(100*time.Millisecond), converterstudio.WithMaxRetries(1))
	api.push("/api/v1/account", stall(), stall())
	started := time.Now()
	_, err := client.Account(ctx)
	var connErr *converterstudio.ConnectionError
	if !errors.As(err, &connErr) || !connErr.Timeout() || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v", err)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Fatalf("took %s", took)
	}
	if n := len(api.of("GET", "/api/v1/account")); n != 2 {
		t.Fatalf("%d attempts", n)
	}
}

func TestTheCallersContextEndsEverything(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	api.push("/api/v1/account", stall())
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := client.Account(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if n := len(api.of("GET", "/api/v1/account")); n != 1 {
		t.Fatalf("retried after the context ended: %d", n)
	}
}

func TestRealPausesEndWithTheContext(t *testing.T) {
	api := newFakeAPI(t)
	client, err := converterstudio.NewClient(converterstudio.WithAPIKey(testKey), converterstudio.WithBaseURL(api.base))
	if err != nil {
		t.Fatal(err)
	}
	busy := `{"error":{"code":"serverBusy","message":"Busy."}}`
	api.push("/api/v1/account", respond(503, busy, "Retry-After", "0.01"))
	if _, err := client.Account(ctx); err != nil {
		t.Fatal(err)
	}
	api.push("/api/v1/account", respond(503, busy, "Retry-After", "30"))
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := client.Account(short); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 5*time.Second {
		t.Fatalf("got %v after %s", err, time.Since(started))
	}
}

func TestBadRequestsAreNotRetried(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	api.push("/api/v1/conversions", respond(400, `{"error":{"code":"invalidCallbackUrl","message":"That callback address cannot be used."}}`))
	_, err := client.CreateConversion(ctx, converterstudio.ConversionParams{
		To: "jpg", URL: "https://example.com/a.heic", CallbackURL: "http://localhost/hook",
	})
	var apiErr *converterstudio.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "invalidCallbackUrl" || !errors.Is(err, converterstudio.ErrBadRequest) {
		t.Fatalf("got %v", err)
	}
	if n := len(api.of("POST", "/api/v1/conversions")); n != 1 {
		t.Fatalf("%d attempts", n)
	}
	if !strings.Contains(string(api.of("POST", "/api/v1/conversions")[0].Body), `"callbackUrl":"http://localhost/hook"`) {
		t.Fatal("no callbackUrl in the body")
	}
}

func TestIdempotentReplay(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	params := converterstudio.ConversionParams{To: "jpg", URL: "https://example.com/a.heic", IdempotencyKey: "clip-42"}
	first, err := client.CreateConversion(ctx, params)
	if err != nil || first.Replayed {
		t.Fatalf("%v %v", first, err)
	}
	again, err := client.CreateConversion(ctx, params)
	if err != nil || !again.Replayed || again.ID != first.ID {
		t.Fatalf("%v %v", again, err)
	}
	for _, p := range api.of("POST", "/api/v1/conversions") {
		if p.Header.Get("Idempotency-Key") != "clip-42" {
			t.Fatal("the caller's key was not sent")
		}
	}
	params.To = "png"
	_, err = client.CreateConversion(ctx, params)
	var apiErr *converterstudio.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "idempotencyKeyReused" || !errors.Is(err, converterstudio.ErrConflict) {
		t.Fatalf("got %v", err)
	}
	for _, bad := range []string{"has space", strings.Repeat("k", 256), "ünicode"} {
		params.IdempotencyKey = bad
		if _, err := client.CreateConversion(ctx, params); err == nil {
			t.Errorf("key %q was accepted", bad)
		}
	}
}

func TestConversionParamsAreChecked(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	for name, p := range map[string]converterstudio.ConversionParams{
		"no target":      {URL: "https://example.com/a.heic"},
		"no source":      {To: "jpg"},
		"two sources":    {To: "jpg", URL: "https://example.com/a.heic", UploadID: "up1"},
		"negative price": {To: "jpg", URL: "https://example.com/a.heic", MaxCredits: -1},
		"quote and max":  {To: "jpg", URL: "https://example.com/a.heic", MaxCredits: 3, QuoteOnly: true},
		"not JSON":       {To: "jpg", URL: "https://example.com/a.heic", Options: map[string]any{"x": func() {}}},
	} {
		if _, err := client.CreateConversion(ctx, p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if n := len(api.all()); n != 0 {
		t.Fatalf("%d requests were sent", n)
	}
}
