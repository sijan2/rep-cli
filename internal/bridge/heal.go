package bridge

import (
	"context"
	"errors"
	"time"
)

// TransportError distinguishes a refused connection from a possibly executed
// request. Even a failed write may have delivered bytes, so it is never retried.
type TransportError struct {
	Dispatched bool
	Cause      error
}

func (e *TransportError) Error() string { return e.Cause.Error() }
func (e *TransportError) Unwrap() error { return e.Cause }
func PossibleDispatch(err error) bool {
	var failure *TransportError
	return errors.As(err, &failure) && failure.Dispatched
}

func (c *Client) Call(ctx context.Context, method string, params interface{}, out interface{}) error {
	err := c.callOnce(ctx, method, params, out)
	var failure *TransportError
	if errors.As(err, &failure) {
		if c != nil && c.Health != nil {
			c.Health(false)
		}
		if !failure.Dispatched && c != nil && c.Reconnect != nil && ctx.Err() == nil {
			wait, cancel := context.WithTimeout(ctx, 3*time.Second)
			registry, reconnectErr := c.Reconnect(wait, c.Registry)
			cancel()
			if reconnectErr == nil && registry.ProfileID != "" && registry.ProfileID == c.Registry.ProfileID && registry.Epoch == c.Registry.Epoch {
				// Do not mutate a client shared by concurrent screenshot workers.
				err = (&Client{Registry: registry}).callOnce(ctx, method, params, out)
			}
		}
	}
	if err == nil && c != nil && c.Health != nil {
		c.Health(true)
	}
	return err
}
