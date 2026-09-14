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
	return c.deviceBuvid != ""
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
		return fmt.Errorf("initializing Bilibili device cookie: %w", deviceInitializationError(err))
	}
	var data struct {
		Buvid3 string `json:"b_3"`
	}
	if err := decodeEnvelope(body, &data); err != nil {
		if apiErr, ok := errors.AsType[*APIError](err); ok {
			apiErr.HTTPStatus = response.StatusCode
		}
		return fmt.Errorf("decoding Bilibili device initialization: %w", deviceInitializationError(err))
	}
	// Do not allow an upstream value to introduce additional Cookie fields, and
	// do not include the device identifier in errors or telemetry.
	if !validDeviceCookie(data.Buvid3) {
		return &APIError{Kind: ErrorSchema, Message: "device initialization returned an invalid buvid3"}
	}
	c.mu.Lock()
	if c.deviceBuvid == "" {
		c.deviceBuvid = data.Buvid3
	}
	c.mu.Unlock()
	return nil
}

func validDeviceCookie(value string) bool {
	cookie := http.Cookie{Name: "buvid3", Value: value}
	return strings.TrimSpace(value) != "" && len(value) <= 256 && cookie.Valid() == nil
}

// The anonymous device endpoint cannot determine whether the account login is
// valid. Keep auth-shaped failures local to the item; preserve risk responses.
func deviceInitializationError(err error) error {
	if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Kind == ErrorAuthentication {
		local := *apiErr
		local.Kind = ErrorTemporary
		return &local
	}
	return err
}
