# Converter Studio for Go

[![Go Reference](https://pkg.go.dev/badge/github.com/tariksucu/converter-studio-go.svg)](https://pkg.go.dev/github.com/tariksucu/converter-studio-go)

The official Go client for the [Converter Studio](https://converter.studio) API. Convert iPhone
photos (HEIC), camera RAW photos, videos, audio, documents, spreadsheets, presentations, e-books,
subtitles, fonts, data files and archives (67 formats) from your Go code. The conversion runs on
Converter Studio's servers, so there is nothing else to install: no ImageMagick, no FFmpeg, no
LibreOffice, no cgo.

```go
client, err := converterstudio.NewClient() // the API key comes from CONVERTER_STUDIO_API_KEY
if err != nil {
	log.Fatal(err)
}
conversion, err := client.ConvertFile(ctx, "IMG_1234.HEIC", "jpg", nil)
if err != nil {
	log.Fatal(err)
}
path, err := client.DownloadFile(ctx, conversion, "photos/", nil) // photos/IMG_1234.jpg
```

- The standard library only: no dependencies.
- Local files, any `io.Reader`, or links (direct links and Google Drive, Dropbox or OneDrive
  share links), with any of the API's conversion settings.
- Large files are uploaded in parts on their own; results are streamed to a file or any
  `io.Writer`.
- A `context.Context` on every call; waiting with the API's long polling and a back-off; retries
  with an `Idempotency-Key`, so a retry never starts or charges a second conversion.
- Errors that carry the API's code and message, for `errors.Is` and `errors.As`.

## Installation

```console
$ go get github.com/tariksucu/converter-studio-go
```

```go
import converterstudio "github.com/tariksucu/converter-studio-go"
```

Go 1.26 or later. Then create an API key on your
[account page](https://converter.studio/account#api-keys) (keys start with `cvs_live_`; keep them
secret) and put it in the environment:

```console
$ export CONVERTER_STUDIO_API_KEY=cvs_live_...
```

Every conversion uses your account's credits, the same as on the website: free accounts get
credits every day, Pro and Premium their monthly credits. Most files cost 1 credit; audio and video
are priced by their length (video also by its frame size and the format made) once Converter
Studio has looked at them. See [pricing](https://converter.studio/pricing).

## Convert a file

```go
ctx := context.Background()
client, err := converterstudio.NewClient() // or NewClient(converterstudio.WithAPIKey("cvs_live_..."))
if err != nil {
	log.Fatal(err)
}

conversion, err := client.ConvertFile(ctx, "talk.mov", "mp4", &converterstudio.ConvertParams{
	Options: map[string]any{"height": "720"},
})
if err != nil {
	log.Fatal(err)
}
fmt.Println(conversion.Status, *conversion.Credits, conversion.OutputName) // done 1 talk.mp4
path, err := client.DownloadFile(ctx, conversion, "out/", nil) // a folder, or a file path
```

`ConvertFile` uploads the file, starts the conversion and waits until it is done (10 minutes at
most by default: `WithWaitTimeout`, or the context's deadline). The source can also be:

- an `io.Reader`, with a name: `client.ConvertReader(ctx, r, "report.docx", "pdf", nil)` (an
  `*os.File` brings its own name);
- a link: `client.ConvertURL(ctx, "https://example.com/photo.heic", "jpg", nil)`. Converter
  Studio fetches the file itself, so it never passes through your machine.

`DownloadFile` never replaces an existing file in a folder unless `Overwrite` is set: the result
gets `-1`, `-2`... before its extension. The file appears only once it is complete. You can also
stream the result to any writer (`client.Download(ctx, conversion, w)`). Results are kept for 6
hours, and anyone with `conversion.DownloadURL` can download the result until
`conversion.ExpiresAt` (printing a `Conversion` leaves the link out).

## Settings

`Options` takes the API's conversion settings, the same as on the website. A few of them:

| Setting                                                          | For                                               |
| ---------------------------------------------------------------- | ------------------------------------------------- |
| `quality`: `"high"`, `"balanced"`, `"small"`                     | video, JPG, WebP, AVIF                            |
| `height`: `"1080"`, `"720"`, `"480"`, `"360"`                    | video (never upscaled)                            |
| `maxSize`: MB                                                    | MP4 that must fit a size                          |
| `maxKb`: KB                                                      | JPG, WebP, AVIF that must fit a size              |
| `longSide`, `resizeWidth`, `resizeHeight`, `fit`                 | resizing images                                   |
| `keepMetadata`, `keepLocation`: `true`                           | keep a photo's camera details and GPS (off)       |
| `bitrate`: `"64"`... `"320"`, `channels`, `trimStart`, `trimEnd` | audio                                             |
| `pageRange`: `"1-3, 5"`, `pdfA`: `true`                          | documents to PDF                                  |
| `ocr`: `true`, `ocrLanguage`                                     | scanned PDFs to Word or text                      |
| `compression`: `"light"`, `"balanced"`, `"strong"`               | PDF to a smaller PDF                              |
| `password`                                                       | a locked ZIP, 7Z or RAR (used once, never stored) |

The full list, and which settings apply to which conversion, is on the
[API page](https://converter.studio/developers). Settings that do not apply to a conversion are
ignored; a name the API does not know is refused (`ErrBadRequest`).

## Formats and account

```go
formats, err := client.Formats(ctx) // no key needed
for _, f := range formats {
	fmt.Println(f.ID, "->", strings.Join(f.Targets, ", "))
}

heic, err := client.Format(ctx, "heic") // what HEIC converts to
account, err := client.Account(ctx)
fmt.Println(account.Plan, account.Credits, account.RenewsAt)
```

## Price limits

A conversion goes ahead on its own when it costs at most `MaxCredits` (20 by default). A dearer
one waits for your go-ahead and costs nothing until then:

```go
conversion, err := client.ConvertFile(ctx, "lecture.mkv", "mp4", &converterstudio.ConvertParams{MaxCredits: 5})
var quoted *converterstudio.ConfirmationRequiredError
if errors.As(err, &quoted) {
	fmt.Printf("It costs %d credits\n", quoted.Credits)
	if _, err := client.Confirm(ctx, quoted.Conversion.ID); err != nil {
		log.Fatal(err)
	}
	conversion, err = client.Wait(ctx, quoted.Conversion.ID)
}
```

`QuoteOnly: true` only gets the price.

## Step by step

`ConvertFile` is built from these, which you can call yourself:

```go
upload, err := client.UploadFile(ctx, "scan.pdf", nil) // large files go in parts
created, err := client.CreateConversion(ctx, converterstudio.ConversionParams{
	To: "docx", UploadID: upload.ID, Options: map[string]any{"ocr": true},
})
created, err = client.CreateConversion(ctx, converterstudio.ConversionParams{
	To: "jpg", URL: "https://example.com/photo.heic",
})
current, err := client.GetConversion(ctx, created.ID)                  // status now
current, err = client.WaitForUpdate(ctx, created.ID, 30*time.Second)  // the API holds the answer up to 30 s
done, err := client.Wait(ctx, created.ID)
```

`Wait` returns a finished conversion or an error (see [Errors](#errors)). It lets the API hold
each status request until something changes, and pauses between answers that come back early (1
second, doubling up to 10).

## Callbacks and retries

```go
created, err := client.CreateConversion(ctx, converterstudio.ConversionParams{
	To:             "mp4",
	URL:            "https://example.com/raw/clip.mov",
	CallbackURL:    "https://example.com/hooks/converter-studio?token=SECRET",
	IdempotencyKey: "clip-42",
})
```

- `CallbackURL`: an `https://` address on the public internet that Converter Studio POSTs to when
  the conversion waits for a go-ahead (`conversion.quoted`) and when it ends (`conversion.done`,
  `conversion.failed` or `conversion.canceled`). The body is
  `{"event", "id", "createdAt", "conversion"}`, with `Webhook-Id` and `Webhook-Timestamp` headers.
  Callbacks are not signed: keep the address secret, and check the conversion with
  `client.GetConversion` before acting on one.
- `IdempotencyKey`: sending the same request again with the same key (within 24 hours) returns the
  first conversion (`conversion.Replayed` is `true`) instead of starting and charging a second
  one; the same key with a different request fails with `ErrConflict`. The client makes up a key
  for each `CreateConversion` call and reuses it for its own retries, so pass your own when your
  code itself may repeat a request (a job queue retrying a task). With a local file, upload it
  once and repeat `CreateConversion` with the same `UploadID` and key.

## Errors

The API's errors are `*converterstudio.APIError` values, and their `Error()` is the API's own
message, a sentence meant for people. They match a sentinel error for their status with
`errors.Is`:

| Error                                                                           | When                                                                                                      |
| ------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `*APIError`                                                                     | the API answered with an error: `StatusCode`, `Code` (`notEnoughCredits`...), `Message`, `Params`         |
| `ErrAuthentication` (401)                                                       | the API key does not work                                                                                 |
| `ErrInsufficientCredits` (402)                                                  | not enough credits (`Params`: `needed`, `balance`)                                                        |
| `ErrFileTooLarge` (413), `ErrUnprocessable` (422)                               | the file is over the plan's limit; the file cannot be converted that way (`unsupportedConversion`)        |
| `ErrBadRequest`, `ErrNotFound`, `ErrConflict`, `ErrGone`, `ErrPermissionDenied` | 400, 404, 409, 410, 403                                                                                   |
| `ErrRateLimited` (429), `ErrServer` (5xx)                                       | retried on their own first (`WithMaxRetries`, 2 by default); `RetryAfter`                                 |
| `*ConversionFailedError`                                                        | the conversion failed, expired or was canceled: `Code`, `Message`, `Conversion`; failures are not charged |
| `*ConfirmationRequiredError`                                                    | the price is over `MaxCredits`: `Credits`, `Conversion`                                                   |
| `*ConversionTimeoutError`                                                       | not done within the wait timeout or the context; it may still finish (`Conversion.ID`)                    |
| `ErrMissingAPIKey`, `*ConnectionError`                                          | no key; the API could not be reached (after the retries; `Timeout()`)                                     |

```go
_, err := client.ConvertFile(ctx, "broken.heic", "jpg", nil)
var failed *converterstudio.ConversionFailedError
var apiErr *converterstudio.APIError
switch {
case errors.As(err, &failed):
	fmt.Println(failed.Code, failed.Message)
case errors.Is(err, converterstudio.ErrInsufficientCredits):
	fmt.Println("Buy credits at https://converter.studio/pricing")
case errors.As(err, &apiErr):
	fmt.Println(apiErr.StatusCode, apiErr.Code, apiErr.Message)
}
```

## Configuration

```go
client, err := converterstudio.NewClient(
	converterstudio.WithAPIKey("cvs_live_..."),                  // default: CONVERTER_STUDIO_API_KEY
	converterstudio.WithBaseURL("https://converter.studio/api"), // default: CONVERTER_STUDIO_BASE_URL or this
	converterstudio.WithTimeout(60*time.Second),                 // a request without progress
	converterstudio.WithMaxRetries(2),                           // network errors, 408, 429, 5xx
	converterstudio.WithWaitTimeout(10*time.Minute),             // how long Wait waits
	converterstudio.WithUserAgent("my-app/1.2"),                 // put in front of the User-Agent
	converterstudio.WithHTTPClient(httpClient),                  // proxies, certificates, tracing
)
```

The timeout is not a limit on a whole call (a large file takes as long as it takes): it ends a
request that goes without progress, and the context limits the call. The API key is only sent to
the base URL; upload and download links carry their own tokens. A `Client` is safe for use by
several goroutines at once.

## Limits

- The largest file depends on the plan (`account.MaxFileBytes`). One request carries up to 95 MB;
  the client sends larger files in parts on its own.
- Audio and video length also depends on the plan (`account.MaxMediaSeconds`).
- Results are kept for 6 hours, then deleted. A status request can wait up to 60 seconds.
- One file in, one file out: no merging or splitting of PDFs.

## Privacy

Your files are sent to Converter Studio (converter.studio) to be converted. Uploaded files are
deleted as soon as they are converted, and results 6 hours later. Each request also says which
client sent it (`X-Converter-Studio-Client: go/<version>`), for usage statistics only. See the
[privacy policy](https://converter.studio/privacy) and the [terms](https://converter.studio/terms).

## Links

- [Converter Studio](https://converter.studio)
- [API documentation](https://converter.studio/developers)
- [Pricing](https://converter.studio/pricing)
- [Source code and issues](https://github.com/tariksucu/converter-studio-go)
- [Changelog](https://github.com/tariksucu/converter-studio-go/blob/main/CHANGELOG.md)

Support: <support@converter.studio>

MIT licensed. © Converter Studio.
