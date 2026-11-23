# Changelog

All notable changes to this module are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module uses
[semantic versioning](https://semver.org/) (tags `vX.Y.Z`).

## v1.0.0 - 2026-11-23

The first release.

- `Client` with functional options (API key from `WithAPIKey` or `CONVERTER_STUDIO_API_KEY`, base
  URL, HTTP client, user agent, timeout, retries, wait timeout) and a `context.Context` on every
  call.
- Convert a local file (`ConvertFile`), an `io.Reader` (`ConvertReader`) or a link (`ConvertURL`)
  with any of the API's settings; wait with long polling and a back-off; save the result to a
  file (`DownloadFile`) or stream it to a writer (`Download`); list formats; read the account's
  plan and credits.
- Uploads in parts for files over 95 MB, retries for network errors, 408, 429 and 5xx answers,
  an `Idempotency-Key` on every conversion, `CallbackURL`, `MaxCredits`, `QuoteOnly` and
  `Confirm`, and typed errors with the API's code and message (`errors.Is`, `errors.As`).
- The standard library only.
