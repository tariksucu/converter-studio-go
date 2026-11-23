package converterstudio

import (
	"encoding/json"
	"fmt"
	"time"
)

// Status is where a conversion stands.
type Status string

// The statuses of a conversion, in the order it goes through them.
const (
	// StatusFetching: Converter Studio is downloading the file from its link.
	StatusFetching Status = "fetching"
	// StatusWaitingForUpload: the upload has not received its file yet.
	StatusWaitingForUpload Status = "waiting_for_upload"
	// StatusMeasuring: audio and video are measured to price them.
	StatusMeasuring Status = "measuring"
	// StatusQuoted: it costs more than its maximum and waits for [Client.Confirm].
	StatusQuoted Status = "quoted"
	// StatusQueued: it waits for a free worker.
	StatusQueued Status = "queued"
	// StatusProcessing: it is being converted.
	StatusProcessing Status = "processing"
	// StatusDone: the result is ready to download.
	StatusDone Status = "done"
	// StatusFailed: it failed; nothing was charged.
	StatusFailed Status = "failed"
	// StatusExpired: the result was deleted after its time ran out.
	StatusExpired Status = "expired"
	// StatusCanceled: it was canceled; nothing was charged.
	StatusCanceled Status = "canceled"
)

// Finished reports whether a conversion in this status will not change any more: done,
// failed, expired or canceled.
func (s Status) Finished() bool {
	switch s {
	case StatusDone, StatusFailed, StatusExpired, StatusCanceled:
		return true
	}
	return false
}

// settled reports whether nobody needs to wait any longer: finished, or waiting for a go-ahead.
func (s Status) settled() bool { return s.Finished() || s == StatusQuoted }

// ErrorInfo says why a conversion failed, as the API says it: a stable code and an English
// message meant to be shown to people.
type ErrorInfo struct {
	// Code is the API's stable error code: "conversionFailed", "noText", "wrongPassword",
	// "remoteNotFound"...
	Code string `json:"code"`
	// Message is a sentence for people.
	Message string `json:"message"`
	// Params holds the values in the message, if any (numbers are float64).
	Params map[string]any `json:"params,omitempty"`
}

// Conversion is a conversion as the API returns it (GET /v1/conversions/{id}). Its ID is also
// the ID of its upload.
type Conversion struct {
	ID     string `json:"id"`
	Status Status `json:"status"`
	// From and To are the formats (such as "heic" and "jpg"); From is empty until known.
	From string `json:"from"`
	To   string `json:"to"`
	// InputName and InputSize describe the file that is converted.
	InputName string `json:"inputName"`
	InputSize int64  `json:"inputSize"`
	// Credits is the price in credits, nil until it is known (audio and video are priced once
	// they have been measured).
	Credits *int `json:"credits"`
	// MaxCredits is the price up to which it goes ahead on its own; nil: it waits for a
	// go-ahead ([Client.Confirm]).
	MaxCredits *int `json:"maxCredits"`
	// Progress goes from 0 to 1 while it converts.
	Progress float64 `json:"progress"`
	// OutputName and OutputSize describe the result, once it is done.
	OutputName string `json:"outputName"`
	OutputSize int64  `json:"outputSize"`
	// DownloadURL is where the result is downloaded from. Anyone with this link can download
	// the result until ExpiresAt, so keep it out of logs.
	DownloadURL string    `json:"downloadUrl"`
	ExpiresAt   time.Time `json:"expiresAt"`
	// Error says why it failed (StatusFailed only).
	Error *ErrorInfo `json:"error"`
	// Next says what to do next, in a sentence.
	Next string `json:"next"`
	// Replayed is true when the API answered a repeated request (the same Idempotency-Key)
	// with this conversion instead of starting a second one.
	Replayed bool `json:"-"`
	// Raw is the JSON the API sent, for fields this package does not name yet.
	Raw json.RawMessage `json:"-"`
}

// Done reports whether the result is ready to download.
func (c *Conversion) Done() bool { return c.Status == StatusDone }

// Finished reports whether the conversion will not change any more.
func (c *Conversion) Finished() bool { return c.Status.Finished() }

// NeedsConfirmation reports whether it costs more than its maximum and waits for
// [Client.Confirm].
func (c *Conversion) NeedsConfirmation() bool { return c.Status == StatusQuoted }

// String describes the conversion without its download link (which works for anyone who has
// it), so that printing a conversion never puts the link in a log.
func (c *Conversion) String() string {
	return fmt.Sprintf("Conversion{ID: %q, Status: %s, From: %q, To: %q, OutputName: %q}",
		c.ID, c.Status, c.From, c.To, c.OutputName)
}

// Upload is an upload slot (POST /v1/uploads). The file is sent to UploadURL; see
// [Client.UploadFile] and [Client.UploadReader], which do it for you.
type Upload struct {
	ID string `json:"id"`
	// UploadURL carries the upload's own token: it needs no API key.
	UploadURL string `json:"uploadUrl"`
	Method    string `json:"method"`
	// MaxBytes is the largest file the account's plan accepts.
	MaxBytes int64 `json:"maxBytes"`
	// PartBytes is the most one request carries; larger files go in parts.
	PartBytes int64     `json:"partBytes"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// String describes the upload without its link (which carries a token).
func (u *Upload) String() string {
	return fmt.Sprintf("Upload{ID: %q, ExpiresAt: %s}", u.ID, u.ExpiresAt.Format(time.RFC3339))
}

// Format is a format the API reads, and what it converts it to right now.
type Format struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Category   string   `json:"category"`
	Extensions []string `json:"extensions"`
	// Targets are the formats it converts to, the most popular first.
	Targets []string `json:"to"`
}

// Allowance is the plan's credits this period.
type Allowance struct {
	Used  int `json:"used"`
	Limit int `json:"limit"`
	// Per is "day" or "month".
	Per string `json:"per"`
}

// Wallet is the web wallet, which pays for cloud work past the credits when allowed.
type Wallet struct {
	// BalanceMicros is the balance in millionths of a US dollar.
	BalanceMicros int64 `json:"balanceMicros"`
	// Pays reports whether it pays once the credits run out.
	Pays bool `json:"pays"`
}

// Account is the account of the API key (GET /v1/account).
type Account struct {
	Email string `json:"email"`
	Plan  string `json:"plan"`
	// AllApps: Converter Studio on every platform (the app plan, Pro or Premium).
	AllApps bool `json:"allApps"`
	// Credits left: the plan's and the packs'.
	Credits   int        `json:"credits"`
	Allowance *Allowance `json:"allowance"`
	// Packs is the credits left in packs (they never expire).
	Packs    int       `json:"packs"`
	RenewsAt time.Time `json:"renewsAt"`
	// MaxFileBytes is the largest file the plan accepts.
	MaxFileBytes int64 `json:"maxFileBytes"`
	// MaxMediaSeconds is the longest audio or video the plan accepts.
	MaxMediaSeconds int     `json:"maxMediaSeconds"`
	Wallet          *Wallet `json:"wallet"`
	// Raw is the JSON the API sent, for fields this package does not name yet.
	Raw json.RawMessage `json:"-"`
}

func decodeConversion(data []byte) (*Conversion, error) {
	var c Conversion
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("converterstudio: unreadable answer from the API: %w", err)
	}
	if c.Status == "" {
		c.Status = StatusFailed
	}
	c.Raw = json.RawMessage(data)
	return &c, nil
}
