package converterstudio_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	converterstudio "github.com/tariksucu/converter-studio-go"
)

func TestAConversionReadsEveryField(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	api.push("/api/v1/conversions/abc", respond(200, `{
		"id": "abc", "status": "done", "from": "mov", "to": "mp4", "inputName": "talk.mov",
		"inputSize": 1234, "credits": 2, "maxCredits": null, "progress": 1, "outputName": "talk.mp4",
		"outputSize": 999, "downloadUrl": "https://converter.studio/api/jobs/abc/download?token=secret",
		"expiresAt": "2026-11-23T07:00:00.000Z", "error": null, "next": "Download it.", "later": 1}`))
	c, err := client.GetConversion(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "abc" || !c.Done() || !c.Finished() || c.NeedsConfirmation() || c.From != "mov" || c.To != "mp4" ||
		c.InputName != "talk.mov" || c.InputSize != 1234 || *c.Credits != 2 || c.MaxCredits != nil ||
		c.Progress != 1 || c.OutputName != "talk.mp4" || c.OutputSize != 999 || c.Error != nil ||
		c.Next != "Download it." || !c.ExpiresAt.Equal(time.Date(2026, 11, 23, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", *c)
	}
	if !strings.Contains(string(c.Raw), `"later": 1`) {
		t.Fatal("the raw JSON is not kept")
	}
	// Printing a conversion never shows its download link.
	for _, printed := range []string{fmt.Sprint(c), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), c.String()} {
		if strings.Contains(printed, "secret") || !strings.Contains(printed, `"abc"`) {
			t.Fatalf("printed %s", printed)
		}
	}
}

func TestAConversionWithNullsAndNoStatus(t *testing.T) {
	api := newFakeAPI(t)
	client, _ := api.client(t)
	api.push("/api/v1/conversions/abc", respond(200, `{"id":"abc","from":null,"credits":null,"inputSize":null,"expiresAt":null}`))
	c, err := client.GetConversion(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != converterstudio.StatusFailed || c.Credits != nil || c.From != "" || !c.ExpiresAt.IsZero() {
		t.Fatalf("%+v", *c)
	}
	api.push("/api/v1/conversions/abc", respond(200, `{"id": 5}`))
	if _, err := client.GetConversion(ctx, "abc"); err == nil || !strings.Contains(err.Error(), "unreadable answer") {
		t.Fatalf("got %v", err)
	}
	api.push("/api/v1/account", respond(200, `not json`))
	if _, err := client.Account(ctx); err == nil || !strings.Contains(err.Error(), "unreadable answer") {
		t.Fatalf("got %v", err)
	}
	api.push("/api/v1/uploads", respond(201, `{"id":"up1"}`))
	if _, err := client.CreateUpload(ctx, "a.txt", 1); err == nil {
		t.Fatal("an upload slot without a link was accepted")
	}
	if _, err := client.CreateUpload(ctx, "", 1); err == nil {
		t.Fatal("an upload without a name was accepted")
	}
}

func TestStatuses(t *testing.T) {
	finished := map[converterstudio.Status]bool{
		converterstudio.StatusFetching: false, converterstudio.StatusWaitingForUpload: false,
		converterstudio.StatusMeasuring: false, converterstudio.StatusQuoted: false,
		converterstudio.StatusQueued: false, converterstudio.StatusProcessing: false,
		converterstudio.StatusDone: true, converterstudio.StatusFailed: true,
		converterstudio.StatusExpired: true, converterstudio.StatusCanceled: true,
	}
	for status, want := range finished {
		if status.Finished() != want {
			t.Errorf("%s: %v", status, !want)
		}
	}
}
