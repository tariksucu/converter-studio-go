package converterstudio_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"time"

	converterstudio "github.com/tariksucu/converter-studio-go"
)

// Convert an iPhone photo to JPG and save it into a folder.
func Example() {
	ctx := context.Background()
	client, err := converterstudio.NewClient() // the API key comes from CONVERTER_STUDIO_API_KEY
	if err != nil {
		log.Fatal(err)
	}
	conversion, err := client.ConvertFile(ctx, "IMG_1234.HEIC", "jpg", nil)
	if err != nil {
		log.Fatal(err)
	}
	path, err := client.DownloadFile(ctx, conversion, "photos/", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("saved", filepath.Base(path))
	// Output: saved IMG_1234.jpg
}

func ExampleNewClient() {
	proxy, _ := url.Parse("http://proxy.internal:3128")
	client, err := converterstudio.NewClient(
		converterstudio.WithAPIKey(os.Getenv("MY_CONVERTER_STUDIO_KEY")), // default: CONVERTER_STUDIO_API_KEY
		converterstudio.WithBaseURL("https://converter.studio/api"),      // default: CONVERTER_STUDIO_BASE_URL or this
		converterstudio.WithTimeout(30*time.Second),                      // without progress, per request
		converterstudio.WithMaxRetries(3),                                // network errors, 408, 429, 5xx
		converterstudio.WithWaitTimeout(15*time.Minute),                  // how long Wait waits
		converterstudio.WithUserAgent("my-app/1.2"),
		converterstudio.WithHTTPClient(&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(client.BaseURL())
	// Output: https://converter.studio/api
}

// Convert a video with a few of the API's settings.
func ExampleClient_ConvertFile() {
	ctx := context.Background()
	client, err := converterstudio.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	conversion, err := client.ConvertFile(ctx, "talk.mov", "mp4", &converterstudio.ConvertParams{
		Options: map[string]any{"height": "720", "quality": "balanced"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(conversion.Status, conversion.OutputName)
	// Output: done talk.mp4
}

func ExampleClient_ConvertReader() {
	ctx := context.Background()
	client, err := converterstudio.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	f, err := os.Open("song.flac")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	// The name tells the API what the bytes are; an *os.File brings its own.
	conversion, err := client.ConvertReader(ctx, f, "", "mp3", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(conversion.OutputName)
	// Output: song.mp3
}

// Converter Studio fetches the file itself, so it never passes through this machine.
func ExampleClient_ConvertURL() {
	ctx := context.Background()
	client, err := converterstudio.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	conversion, err := client.ConvertURL(ctx, "https://example.com/report.docx", "pdf", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(conversion.OutputName)
	// Output: report.pdf
}

// The steps of ConvertFile, one by one.
func ExampleClient_UploadFile() {
	ctx := context.Background()
	client, err := converterstudio.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	upload, err := client.UploadFile(ctx, "scan.pdf", nil) // large files go in parts
	if err != nil {
		log.Fatal(err)
	}
	created, err := client.CreateConversion(ctx, converterstudio.ConversionParams{
		To: "docx", UploadID: upload.ID, Options: map[string]any{"ocr": true},
	})
	if err != nil {
		log.Fatal(err)
	}
	done, err := client.Wait(ctx, created.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(done.Status, done.OutputName)
	// Output: done scan.docx
}

// A callback, and a key that makes a repeated request return the first conversion.
func ExampleClient_CreateConversion() {
	ctx := context.Background()
	client, err := converterstudio.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	params := converterstudio.ConversionParams{
		To:             "mp4",
		URL:            "https://example.com/raw/clip.mov",
		CallbackURL:    "https://example.com/hooks/converter-studio?token=SECRET",
		IdempotencyKey: "clip-42",
	}
	created, err := client.CreateConversion(ctx, params)
	if err != nil {
		log.Fatal(err)
	}
	again, err := client.CreateConversion(ctx, params) // a job queue running the task twice
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(again.ID == created.ID, again.Replayed)
	// Output: true true
}

func ExampleClient_DownloadFile() {
	ctx := context.Background()
	client, err := converterstudio.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	conversion, err := client.ConvertFile(ctx, "IMG_1234.HEIC", "jpg", nil)
	if err != nil {
		log.Fatal(err)
	}
	for range 2 {
		// A folder: the result keeps its name, and an existing file is never replaced.
		path, err := client.DownloadFile(ctx, conversion, "out/", nil)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(filepath.Base(path))
	}
	// Output:
	// IMG_1234.jpg
	// IMG_1234-1.jpg
}

func ExampleClient_Download() {
	ctx := context.Background()
	client, err := converterstudio.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	conversion, err := client.ConvertFile(ctx, "IMG_1234.HEIC", "webp", nil)
	if err != nil {
		log.Fatal(err)
	}
	var buf bytes.Buffer // or any io.Writer: an HTTP response, an object store's upload
	n, err := client.Download(ctx, conversion, &buf)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(n == conversion.OutputSize)
	// Output: true
}

func ExampleClient_Format() {
	ctx := context.Background()
	client, err := converterstudio.NewClient() // listing formats needs no key
	if err != nil {
		log.Fatal(err)
	}
	heic, err := client.Format(ctx, "heic")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(heic.Name, "converts to JPG:", slices.Contains(heic.Targets, "jpg"))
	// Output: HEIC converts to JPG: true
}

func ExampleClient_Formats() {
	ctx := context.Background()
	client, err := converterstudio.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	formats, err := client.Formats(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, format := range formats {
		fmt.Println(format.ID, "->", format.Targets)
	}
}

func ExampleClient_Account() {
	ctx := context.Background()
	client, err := converterstudio.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	account, err := client.Account(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(account.Plan, account.Credits, "credits left, renews", account.RenewsAt.Format(time.DateOnly))
}

// A conversion that costs more than MaxCredits waits for a go-ahead and costs nothing until
// then.
func ExampleConfirmationRequiredError() {
	ctx := context.Background()
	client, err := converterstudio.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	conversion, err := client.ConvertFile(ctx, "lecture.mkv", "mp4", &converterstudio.ConvertParams{MaxCredits: 5})
	var quoted *converterstudio.ConfirmationRequiredError
	if errors.As(err, &quoted) {
		fmt.Printf("It costs %d credits\n", quoted.Credits)
		if _, err := client.Confirm(ctx, quoted.Conversion.ID); err != nil {
			log.Fatal(err)
		}
		conversion, err = client.Wait(ctx, quoted.Conversion.ID)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(conversion.Status)
	// Output:
	// It costs 7 credits
	// done
}

func ExampleAPIError() {
	ctx := context.Background()
	client, err := converterstudio.NewClient(converterstudio.WithAPIKey("cvs_live_wrong"))
	if err != nil {
		log.Fatal(err)
	}
	_, err = client.Account(ctx)
	var apiErr *converterstudio.APIError
	if errors.As(err, &apiErr) {
		fmt.Println(apiErr.StatusCode, apiErr.Code, errors.Is(err, converterstudio.ErrAuthentication))
	}
	// Output: 401 invalidApiKey true
}

func ExampleConversionFailedError() {
	ctx := context.Background()
	client, err := converterstudio.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	_, err = client.ConvertFile(ctx, "broken.jpg", "png", nil)
	var failed *converterstudio.ConversionFailedError
	if errors.As(err, &failed) {
		fmt.Println(failed.Code) // and failed.Message, a sentence for people
	}
	// Output: conversionFailed
}
