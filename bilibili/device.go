package bilibili

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func (c *Client) hasDeviceCookie() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return strings.TrimSpace(c.cookies["buvid3"]) != "" || c.deviceBuvid != ""
}

// ensureDeviceCookie supplies the normal web device cookie required by opus
// details, independently of account authentication. Cache it for this client,
// including across session renewals, without changing persisted login secrets.
func (c *Client) ensureDeviceCookie(ctx context.Context) error {
	if c.hasDeviceCookie() {
		return nil
	}
	// Serialize initialization while allowing callers waiting on another request
	// to cancel. Failed initialization is retried by the normal collection retry.
	select {
	case c.deviceInit <- struct{}{}:
		defer func() { <-c.deviceInit }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if c.hasDeviceCookie() {
		return nil
	}
	response, body, err := c.get(ctx, c.apiURL+"/x/frontend/finger/spi", nil, false)
	if err != nil {
		return fmt.Errorf("initializing Bilibili device cookie: %w", err)
	}
	var data struct {
		Buvid3 string `json:"b_3"`
	}
	if err := decodeEnvelope(body, &data); err != nil {
		if apiErr, ok := errors.AsType[*APIError](err); ok {
			apiErr.HTTPStatus = response.StatusCode
		}
		return fmt.Errorf("decoding Bilibili device initialization: %w", err)
	}
	// Do not allow an upstream value to introduce additional Cookie fields, and
	// do not include the device identifier in errors or telemetry.
	cookie := http.Cookie{Name: "buvid3", Value: data.Buvid3}
	if strings.TrimSpace(data.Buvid3) == "" || len(data.Buvid3) > 256 || cookie.Valid() != nil {
		return &APIError{Kind: ErrorSchema, Message: "device initialization returned an invalid buvid3"}
	}
	c.mu.Lock()
	c.deviceBuvid = data.Buvid3
	c.mu.Unlock()
	return nil
}
