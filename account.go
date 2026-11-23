package converterstudio

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Formats returns the formats the API reads and what each converts to right now
// (GET /v1/formats). It needs no API key.
func (c *Client) Formats(ctx context.Context) ([]Format, error) {
	return c.formats(ctx, nil)
}

// Format returns one format (such as "heic") and what it converts to right now. A format
// the API does not know fails with [ErrBadRequest]; one it cannot convert right now with
// [ErrNotFound]. It needs no API key.
func (c *Client) Format(ctx context.Context, id string) (*Format, error) {
	id = strings.ToLower(strings.TrimLeft(strings.TrimSpace(id), "."))
	formats, err := c.formats(ctx, url.Values{"from": {id}})
	if err != nil {
		return nil, err
	}
	for i := range formats {
		if formats[i].ID == id {
			return &formats[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s converts to nothing right now", ErrNotFound, id)
}

func (c *Client) formats(ctx context.Context, query url.Values) ([]Format, error) {
	k := &call{method: http.MethodGet, url: c.apiURL("/v1/formats", query), auth: c.apiKey != ""}
	data, _, err := c.callJSON(ctx, k)
	if err != nil {
		return nil, err
	}
	var body struct {
		Formats []Format `json:"formats"`
	}
	if err := decodeJSON(data, &body); err != nil {
		return nil, err
	}
	return body.Formats, nil
}

// Account returns the API key's account: plan, credits left and limits (GET /v1/account).
func (c *Client) Account(ctx context.Context) (*Account, error) {
	data, _, err := c.callJSON(ctx, &call{method: http.MethodGet, url: c.apiURL("/v1/account", nil), auth: true})
	if err != nil {
		return nil, err
	}
	var account Account
	if err := decodeJSON(data, &account); err != nil {
		return nil, err
	}
	account.Raw = data
	return &account, nil
}
