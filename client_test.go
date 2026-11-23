package converterstudio_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	converterstudio "github.com/tariksucu/converter-studio-go"
)

var ctx = context.Background()

func TestEveryRequestNamesTheClientAndOnlyAPIRequestsCarryTheKey(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t, converterstudio.WithUserAgent("my-app/1.2"))
	conversion, err := client.ConvertFile(ctx, writeFile(t, "photo.heic", "HEIC"), "jpg", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Download(ctx, conversion, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	requests := api.all()
	if len(requests) < 5 {
		t.Fatalf("only %d requests", len(requests))
	}
	for _, r := range requests {
		if got := r.Header.Get("X-Converter-Studio-Client"); got != "go/"+converterstudio.Version {
			t.Errorf("%s %s: client header %q", r.Method, r.Path, got)
		}
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "my-app/1.2 converter-studio-go/") {
			t.Errorf("%s %s: User-Agent %q", r.Method, r.Path, ua)
		}
		auth := r.Header.Get("Authorization")
		toAPI := strings.HasPrefix(r.Path, "/api/v1/") && r.Method != http.MethodPut
		if toAPI && auth != "Bearer "+testKey {
			t.Errorf("%s %s: no key", r.Method, r.Path)
		}
		if !toAPI && auth != "" {
			t.Errorf("%s %s: the key went to a link", r.Method, r.Path)
		}
	}
}

func TestFormatsNeedNoKey(t *testing.T) {
	api := newFakeAPI(t)
	client, err := converterstudio.NewClient(converterstudio.WithAPIKey(""), converterstudio.WithBaseURL(api.base))
	if err != nil {
		t.Fatal(err)
	}
	formats, err := client.Formats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(formats) != 2 || formats[0].ID != "heic" || formats[0].Targets[0] != "jpg" || formats[0].Extensions[1] != "heif" {
		t.Fatalf("formats: %+v", formats)
	}
	heic, err := client.Format(ctx, ".HEIC")
	if err != nil || heic.Name != "HEIC" {
		t.Fatalf("format: %+v %v", heic, err)
	}
	if got := api.of("GET", "/api/v1/formats")[1].Query.Get("from"); got != "heic" {
		t.Fatalf("from=%q", got)
	}
	if auth := api.of("GET", "/api/v1/formats")[0].Header.Get("Authorization"); auth != "" {
		t.Fatalf("a key was sent: %q", auth)
	}
	if _, err := client.Format(ctx, "pdf"); !errors.Is(err, converterstudio.ErrNotFound) {
		t.Fatalf("a format without targets: %v", err)
	}
	if _, err := client.Format(ctx, "nope"); !errors.Is(err, converterstudio.ErrBadRequest) {
		t.Fatalf("an unknown format: %v", err)
	}
}

func TestWithoutAKeyNothingIsSent(t *testing.T) {
	t.Setenv(converterstudio.APIKeyEnv, "")
	api := newFakeAPI(t)
	client, err := converterstudio.NewClient(converterstudio.WithBaseURL(api.base))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Account(ctx); !errors.Is(err, converterstudio.ErrMissingAPIKey) {
		t.Fatalf("got %v", err)
	}
	if _, err := client.ConvertURL(ctx, "https://example.com/a.heic", "jpg", nil); !errors.Is(err, converterstudio.ErrMissingAPIKey) {
		t.Fatalf("got %v", err)
	}
	if n := len(api.all()); n != 0 {
		t.Fatalf("%d requests were sent", n)
	}
	if !strings.Contains(converterstudio.ErrMissingAPIKey.Error(), "https://converter.studio/account#api-keys") {
		t.Fatal("the error does not say where keys are made")
	}
}

func TestTheKeyAndAddressComeFromTheEnvironment(t *testing.T) {
	api := newFakeAPI(t)
	t.Setenv(converterstudio.APIKeyEnv, "  "+testKey+"\n")
	t.Setenv(converterstudio.BaseURLEnv, api.base+"/")
	client, err := converterstudio.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	if client.BaseURL() != api.base {
		t.Fatalf("base URL %q", client.BaseURL())
	}
	if _, err := client.Account(ctx); err != nil {
		t.Fatal(err)
	}
	// An explicit option wins, even an empty key.
	client, _ = converterstudio.NewClient(converterstudio.WithAPIKey(""))
	if _, err := client.Account(ctx); !errors.Is(err, converterstudio.ErrMissingAPIKey) {
		t.Fatalf("got %v", err)
	}
	// A wrong address in the environment does not matter when one is given.
	t.Setenv(converterstudio.BaseURLEnv, "ftp://example.com")
	if _, err := converterstudio.NewClient(); err == nil || !strings.Contains(err.Error(), converterstudio.BaseURLEnv) {
		t.Fatalf("got %v", err)
	}
	if _, err := converterstudio.NewClient(converterstudio.WithBaseURL(api.base)); err != nil {
		t.Fatal(err)
	}
	t.Setenv(converterstudio.BaseURLEnv, "")
	client, _ = converterstudio.NewClient()
	if client.BaseURL() != converterstudio.DefaultBaseURL {
		t.Fatalf("default base URL %q", client.BaseURL())
	}
}

func TestOptionsAreChecked(t *testing.T) {
	for name, opt := range map[string]converterstudio.Option{
		"scheme":       converterstudio.WithBaseURL("ftp://example.com/api"),
		"no host":      converterstudio.WithBaseURL("https://"),
		"query":        converterstudio.WithBaseURL("https://example.com/api?x=1"),
		"user":         converterstudio.WithBaseURL("https://me:pw@example.com/api"),
		"http client":  converterstudio.WithHTTPClient(nil),
		"timeout":      converterstudio.WithTimeout(0),
		"retries":      converterstudio.WithMaxRetries(-1),
		"wait timeout": converterstudio.WithWaitTimeout(-time.Second),
		"user agent":   converterstudio.WithUserAgent("a\r\nX-Evil: 1"),
	} {
		if _, err := converterstudio.NewClient(opt); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	client, err := converterstudio.NewClient(nil, converterstudio.WithBaseURL(" http://localhost:4391/api/ "))
	if err != nil || client.BaseURL() != "http://localhost:4391/api" {
		t.Fatalf("%v %v", client, err)
	}
	if ua := converterstudio.UserAgent(); !strings.HasPrefix(ua, "converter-studio-go/"+converterstudio.Version+" (go") {
		t.Fatalf("User-Agent %q", ua)
	}
}

func TestAWrongKeyGivesTheAPIMessage(t *testing.T) {
	api := newFakeAPI(t)
	client, err := converterstudio.NewClient(converterstudio.WithAPIKey("cvs_live_wrong"), converterstudio.WithBaseURL(api.base))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Account(ctx)
	var apiErr *converterstudio.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 || apiErr.Code != "invalidApiKey" {
		t.Fatalf("got %#v", err)
	}
	if !errors.Is(err, converterstudio.ErrAuthentication) || errors.Is(err, converterstudio.ErrNotFound) {
		t.Fatal("errors.Is does not match the status")
	}
	if !strings.HasPrefix(err.Error(), "This API key does not work.") {
		t.Fatalf("message %q", err)
	}
	if n := len(api.of("GET", "/api/v1/account")); n != 1 {
		t.Fatalf("a 401 was retried: %d requests", n)
	}
}

func TestAccount(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	account, err := client.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if account.Plan != "pro" || account.Credits != 1099 || !account.AllApps || account.Allowance.Limit != 1100 ||
		account.Allowance.Per != "month" || account.MaxFileBytes != 1<<30 || account.MaxMediaSeconds != 7200 ||
		account.Wallet.BalanceMicros != 1500000 || !account.Wallet.Pays || account.RenewsAt.Year() != 2026 {
		t.Fatalf("account: %+v", account)
	}
	if !strings.Contains(string(account.Raw), `"newField":"kept"`) {
		t.Fatal("the raw JSON is not kept")
	}
}

func TestConvertAFile(t *testing.T) {
	api := newFakeAPI(t)
	api.output = []byte("JPEGDATA")
	client, pauses := api.client(t)
	source := writeFile(t, "IMG_1234.HEIC", "heic bytes")
	conversion, err := client.ConvertFile(ctx, source, ".JPG", &converterstudio.ConvertParams{
		Options: map[string]any{"quality": "high", "keepMetadata": true}, MaxCredits: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !conversion.Done() || conversion.OutputName != "IMG_1234.jpg" || *conversion.Credits != 1 ||
		conversion.From != "heic" || conversion.InputSize != 10 || conversion.OutputSize != 8 || conversion.Replayed {
		t.Fatalf("conversion: %+v", conversion)
	}
	upload := api.of("POST", "/api/v1/uploads")[0]
	if string(upload.Body) != `{"fileName":"IMG_1234.HEIC","size":10}` {
		t.Fatalf("upload body %s", upload.Body)
	}
	put := api.of("PUT", "/api/v1/uploads/up00000001")[0]
	if string(put.Body) != "heic bytes" || put.Header.Get("Upload-Offset") != "" ||
		put.Header.Get("Content-Type") != "application/octet-stream" || put.Header.Get("Content-Length") != "10" {
		t.Fatalf("upload: %q %v", put.Body, put.Header)
	}
	create := api.of("POST", "/api/v1/conversions")[0]
	want := `{"maxCredits":5,"options":{"keepMetadata":true,"quality":"high"},"to":"jpg","uploadId":"up00000001"}`
	if string(create.Body) != want+"\n" && string(create.Body) != want {
		t.Fatalf("conversion body %s", create.Body)
	}
	if key := create.Header.Get("Idempotency-Key"); !strings.HasPrefix(key, "cvs-go-") || len(key) != 39 {
		t.Fatalf("idempotency key %q", key)
	}
	polls := api.of("GET", "/api/v1/conversions/up00000001")
	if len(polls) != 2 || polls[0].Query.Get("wait") != "25" {
		t.Fatalf("polls: %+v", polls)
	}
	// The first status request answered early (processing): one pause of a second.
	if len(*pauses) != 1 || (*pauses)[0] != time.Second {
		t.Fatalf("pauses: %v", *pauses)
	}
	var b strings.Builder
	if n, err := client.Download(ctx, conversion, &b); err != nil || n != 8 || b.String() != "JPEGDATA" {
		t.Fatalf("download: %d %q %v", n, b.String(), err)
	}
}

func TestConvertAReader(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	conversion, err := client.ConvertReader(ctx, strings.NewReader("png bytes"), "logo.png", "webp", nil)
	if err != nil || conversion.OutputName != "logo.webp" {
		t.Fatalf("%v %v", conversion, err)
	}
	f, err := os.Open(writeFile(t, "report.docx", "docx bytes"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	conversion, err = client.ConvertReader(ctx, f, "", "pdf", nil)
	if err != nil || conversion.OutputName != "report.pdf" || conversion.InputSize != 10 {
		t.Fatalf("%v %v", conversion, err)
	}
	if _, err := client.ConvertReader(ctx, strings.NewReader("x"), "", "pdf", nil); err == nil {
		t.Fatal("a reader without a name was accepted")
	}
	if _, err := client.ConvertFile(ctx, filepath.Join(t.TempDir(), "missing.heic"), "jpg", nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing file: %v", err)
	}
	if _, err := client.ConvertFile(ctx, t.TempDir(), "jpg", nil); err == nil {
		t.Fatal("a folder was accepted")
	}
	if _, err := client.ConvertFile(ctx, "x.heic", "", nil); err == nil {
		t.Fatal("no target format was accepted")
	}
}

func TestConvertALink(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	conversion, err := client.ConvertURL(ctx, "https://example.com/files/clip.mov", "mp4", nil)
	if err != nil || conversion.OutputName != "clip.mp4" {
		t.Fatalf("%v %v", conversion, err)
	}
	body := string(api.of("POST", "/api/v1/conversions")[0].Body)
	if !strings.Contains(body, `"url":"https://example.com/files/clip.mov"`) || strings.Contains(body, "uploadId") {
		t.Fatalf("body %s", body)
	}
	if len(api.of("POST", "/api/v1/uploads")) != 0 {
		t.Fatal("a link was uploaded")
	}
}

func TestWithoutWaiting(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	created, err := client.ConvertURL(ctx, "https://example.com/a.heic", "jpg", &converterstudio.ConvertParams{NoWait: true})
	if err != nil || created.Status != converterstudio.StatusFetching {
		t.Fatalf("%v %v", created, err)
	}
	if n := len(api.of("GET", "/api/v1/conversions/"+created.ID)); n != 0 {
		t.Fatalf("%d status requests", n)
	}
	if _, err := client.Download(ctx, created, &strings.Builder{}); !errors.Is(err, converterstudio.ErrNotDone) {
		t.Fatalf("download before done: %v", err)
	}
	done, err := client.Wait(ctx, created.ID)
	if err != nil || !done.Done() {
		t.Fatalf("%v %v", done, err)
	}
}

func TestGetConversionAndWaitForUpdate(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	created, _ := client.ConvertURL(ctx, "https://example.com/a.heic", "jpg", &converterstudio.ConvertParams{NoWait: true})
	now, err := client.GetConversion(ctx, created.ID)
	if err != nil || now.Status != converterstudio.StatusProcessing {
		t.Fatalf("%v %v", now, err)
	}
	if _, err := client.WaitForUpdate(ctx, created.ID, 90*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := client.WaitForUpdate(ctx, created.ID, 0); err != nil {
		t.Fatal(err)
	}
	gets := api.of("GET", "/api/v1/conversions/"+created.ID)
	if gets[0].Query.Has("wait") || gets[1].Query.Get("wait") != "60" || gets[2].Query.Get("wait") != "1" {
		t.Fatalf("wait values: %v %v %v", gets[0].Query, gets[1].Query, gets[2].Query)
	}
	if _, err := client.GetConversion(ctx, "unknown"); !errors.Is(err, converterstudio.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if _, err := client.GetConversion(ctx, ""); err == nil {
		t.Fatal("an empty id was accepted")
	}
	// The id is escaped in the path.
	_, _ = client.GetConversion(ctx, "a/b")
	if got := api.all()[len(api.all())-1].RawPath; got != "/api/v1/conversions/a%2Fb" {
		t.Fatalf("path seen by the server: %s", got)
	}
}

func writeFile(t testing.TB, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
