package converterstudio_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	converterstudio "github.com/tariksucu/converter-studio-go"
)

func convertOne(t *testing.T, api *fakeAPI, client *converterstudio.Client, link string) *converterstudio.Conversion {
	t.Helper()
	conversion, err := client.ConvertURL(ctx, link, "jpg", nil)
	if err != nil {
		t.Fatal(err)
	}
	return conversion
}

func TestDownloadIntoAFolder(t *testing.T) {
	api := newFakeAPI(t)
	api.output = []byte("JPEG")
	client, _ := api.client(t)
	conversion := convertOne(t, api, client, "https://example.com/IMG_1.heic")
	dir := t.TempDir()
	first, err := client.DownloadFile(ctx, conversion, dir, nil)
	if err != nil || first != filepath.Join(dir, "IMG_1.jpg") {
		t.Fatalf("%s %v", first, err)
	}
	second, err := client.DownloadFile(ctx, conversion, dir+string(os.PathSeparator), nil)
	if err != nil || second != filepath.Join(dir, "IMG_1-1.jpg") {
		t.Fatalf("%s %v", second, err)
	}
	third, err := client.DownloadFile(ctx, conversion, dir, &converterstudio.DownloadOptions{Overwrite: true})
	if err != nil || third != first {
		t.Fatalf("%s %v", third, err)
	}
	// A folder that does not exist yet, named with a trailing slash.
	fresh, err := client.DownloadFile(ctx, conversion, filepath.Join(dir, "new", "deeper")+"/", nil)
	if err != nil || fresh != filepath.Join(dir, "new", "deeper", "IMG_1.jpg") {
		t.Fatalf("%s %v", fresh, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Fatalf("a temporary file was left: %s", e.Name())
		}
	}
	if data, _ := os.ReadFile(first); string(data) != "JPEG" {
		t.Fatalf("saved %q", data)
	}
	// .tar.gz keeps its double extension when numbered.
	api.outputName = "backup.tar.gz"
	archive := convertOne(t, api, client, "https://example.com/backup.zip")
	for _, want := range []string{"backup.tar.gz", "backup-1.tar.gz"} {
		got, err := client.DownloadFile(ctx, archive, dir, nil)
		if err != nil || filepath.Base(got) != want {
			t.Fatalf("%s %v", got, err)
		}
	}
}

func TestDownloadToAPath(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	conversion := convertOne(t, api, client, "https://example.com/a.heic")
	path := filepath.Join(t.TempDir(), "photo.jpeg")
	if got, err := client.DownloadFile(ctx, conversion, path, nil); err != nil || got != path {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := client.DownloadFile(ctx, conversion, path, nil); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("an existing file was replaced: %v", err)
	}
	if _, err := client.DownloadFile(ctx, conversion, path, &converterstudio.DownloadOptions{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	// The current folder when no destination is given.
	t.Chdir(t.TempDir())
	if got, err := client.DownloadFile(ctx, conversion, "", nil); err != nil || got != "a.jpg" {
		t.Fatalf("%s %v", got, err)
	}
}

func TestAResultNameCannotLeaveTheFolder(t *testing.T) {
	api := newFakeAPI(t)
	api.outputName = "../../etc/pass\x00wd.jpg"
	client, _ := api.client(t)
	conversion := convertOne(t, api, client, "https://example.com/a.heic")
	dir := t.TempDir()
	got, err := client.DownloadFile(ctx, conversion, dir, nil)
	if err != nil || got != filepath.Join(dir, "pass_wd.jpg") {
		t.Fatalf("%s %v", got, err)
	}
	for name, want := range map[string]string{
		"..\\..\\x.jpg": "x.jpg", "  .hidden.  ": "hidden", "": "fallback", "..": "fallback", "a\nb.jpg": "a_b.jpg",
	} {
		if got := converterstudio.SafeFileName(name, "fallback"); got != want {
			t.Errorf("%q: %q", name, got)
		}
	}
}

func TestADownloadThatFailsLeavesNothing(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	conversion := convertOne(t, api, client, "https://example.com/a.heic")
	api.push("/api/jobs/"+conversion.ID+"/download", respond(410, `{"error":"fileExpired"}`))
	dir := t.TempDir()
	_, err := client.DownloadFile(ctx, conversion, dir, nil)
	var apiErr *converterstudio.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "fileExpired" || !errors.Is(err, converterstudio.ErrGone) ||
		err.Error() != "This file has already been deleted. Please convert it again." {
		t.Fatalf("got %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}

func TestABrokenDownloadIsStartedAgain(t *testing.T) {
	api := newFakeAPI(t)
	api.output = []byte("the whole result")
	client, pauses := api.client(t, noReuse())
	conversion := convertOne(t, api, client, "https://example.com/a.heic")
	path := "/api/jobs/" + conversion.ID + "/download"
	api.push(path, cutShort([]byte("the who")))
	dir := t.TempDir()
	got, err := client.DownloadFile(ctx, conversion, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(got); string(data) != "the whole result" {
		t.Fatalf("saved %q", data)
	}
	if len(api.of("GET", path)) != 2 || len(*pauses) == 0 {
		t.Fatal("not started again")
	}
	// To a writer, a stream that broke after writing is not sent again.
	api.push(path, cutShort([]byte("the who")))
	var b strings.Builder
	n, err := client.Download(ctx, conversion, &b)
	var connErr *converterstudio.ConnectionError
	if !errors.As(err, &connErr) || n != 7 || b.String() != "the who" {
		t.Fatalf("%d %q %v", n, b.String(), err)
	}
	// Before anything was written, it is.
	api.push(path, hangUp())
	b.Reset()
	if n, err := client.Download(ctx, conversion, &b); err != nil || n != 16 {
		t.Fatalf("%d %v", n, err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestAWriterErrorIsNotRetried(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	conversion := convertOne(t, api, client, "https://example.com/a.heic")
	if _, err := client.Download(ctx, conversion, failingWriter{}); err == nil || err.Error() != "disk full" {
		t.Fatalf("got %v", err)
	}
	if n := len(api.of("GET", "/api/jobs/"+conversion.ID+"/download")); n != 1 {
		t.Fatalf("%d attempts", n)
	}
}

func TestOnlyDoneConversionsDownload(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	for _, c := range []*converterstudio.Conversion{
		{ID: "x", Status: converterstudio.StatusProcessing},
		{ID: "x", Status: converterstudio.StatusQuoted},
		{ID: "x", Status: converterstudio.StatusDone},
		nil,
	} {
		if _, err := client.Download(ctx, c, io.Discard); err == nil {
			t.Errorf("%v: downloaded", c)
		}
		if _, err := client.DownloadFile(ctx, c, t.TempDir(), nil); err == nil {
			t.Errorf("%v: downloaded", c)
		}
	}
	if n := len(api.all()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}
