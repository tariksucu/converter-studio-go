// Package converterstudio is the official Go client for the [Converter Studio] API.
//
// Converter Studio converts iPhone photos (HEIC), camera RAW photos, videos, audio,
// documents, spreadsheets, presentations, e-books, subtitles, fonts, data files and archives
// (67 formats). The conversion runs on Converter Studio's servers, so there is nothing else to
// install: no ImageMagick, no FFmpeg, no LibreOffice. The package uses the standard library
// only.
//
//	client, err := converterstudio.NewClient() // the API key comes from CONVERTER_STUDIO_API_KEY
//	if err != nil {
//		log.Fatal(err)
//	}
//	conversion, err := client.ConvertFile(ctx, "IMG_1234.HEIC", "jpg", nil)
//	if err != nil {
//		log.Fatal(err)
//	}
//	path, err := client.DownloadFile(ctx, conversion, "photos/", nil) // photos/IMG_1234.jpg
//
// # API key and credits
//
// Create an API key on your [account page] (keys start with "cvs_live_"; keep them secret)
// and put it in the CONVERTER_STUDIO_API_KEY environment variable, or pass it with
// [WithAPIKey]. Listing formats needs no key. Every conversion uses the account's credits, the
// same as on the website: most files cost 1 credit; audio and video are priced by their length
// once Converter Studio has looked at them. See [pricing].
//
// # Converting
//
// [Client.ConvertFile] (a local file), [Client.ConvertReader] (an [io.Reader]) and
// [Client.ConvertURL] (a link, which Converter Studio fetches itself) upload the file, start
// the conversion and wait until it is done. [ConvertParams] takes the API's conversion
// settings, such as {"quality": "high"} for video and JPG, {"height": "720"} for video,
// {"keepMetadata": true} for photos, {"pageRange": "1-3, 5"} for documents to PDF or
// {"ocr": true} for scanned PDFs to Word; the full list is in the [API documentation]. Save the
// result with [Client.DownloadFile] or stream it with [Client.Download]. Results are kept for
// 6 hours.
//
// The steps can also be taken one by one: [Client.UploadFile] (large files go in parts on
// their own), [Client.CreateConversion], [Client.GetConversion] and [Client.Wait], which lets
// the API hold each status request until something changes (long polling) and pauses with a
// back-off between answers that come back early.
//
// # Prices, callbacks and retries
//
// A conversion goes ahead on its own when it costs at most MaxCredits (20 by default). A
// dearer one fails with a [*ConfirmationRequiredError], costs nothing, and goes ahead with
// [Client.Confirm]. QuoteOnly only gets the price.
//
// CallbackURL asks Converter Studio to POST to your server when a conversion waits for a
// go-ahead and when it ends. Requests that fail on the network or with a 408, 429 or 5xx
// answer are sent again ([WithMaxRetries]); conversions are created with an Idempotency-Key,
// so a retry never starts or charges a second one.
//
// # Errors
//
// Errors of the API are [*APIError] values carrying the API's code ("notEnoughCredits"...)
// and its message, a sentence meant for people; they match sentinel errors such as
// [ErrAuthentication] and [ErrInsufficientCredits] with [errors.Is]. A failed conversion is a
// [*ConversionFailedError], one that did not finish in time a [*ConversionTimeoutError], and an
// unreachable API a [*ConnectionError]. Use [errors.As] to read them.
//
// # Privacy
//
// Files are sent to Converter Studio (converter.studio) to be converted. Uploaded files are
// deleted as soon as they are converted, and results 6 hours later. Each request says which
// client sent it (X-Converter-Studio-Client: go/<version>), for usage statistics only. The API
// key is sent to the API's address only; upload and download links carry their own tokens.
// See the [privacy policy].
//
// [Converter Studio]: https://converter.studio
// [API documentation]: https://converter.studio/developers
// [account page]: https://converter.studio/account#api-keys
// [pricing]: https://converter.studio/pricing
// [privacy policy]: https://converter.studio/privacy
package converterstudio
