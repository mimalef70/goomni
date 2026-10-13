package rubikameow

import (
	"context"
	"errors"
	"github.com/mimalef70/goomni/src/domains"
)

// A rejection while reading one peer is not enough evidence to erase an
// authenticated account. Confirm loss against the already-bound self identity.
// A transient validation failure retains the session and degraded recovery.
func (c *Client) recoveryAuthRequired(ctx context.Context, cause error) bool {
	var rejection *domains.Error
	if !errors.As(cause, &rejection) || rejection.Code != "AUTH_REQUIRED" {
		return false
	}
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	profile, err := c.invoke(ctx, "getUserInfo", object{"user_guid": account}, false, "")
	if err != nil {
		return errors.As(err, &rejection) && rejection.Code == "AUTH_REQUIRED"
	}
	observed := asObject(profile["user"]).str("user_guid")
	return observed != "" && observed != account
}

// A peer-specific read failure is retried without discarding its cursor. An
// auth rejection is peer-specific only after a successful bound-self read.
func (c *Client) retryRecoveryPeer(ctx context.Context, cause error) bool {
	var rejection *domains.Error
	if !errors.As(cause, &rejection) {
		return false
	}
	switch rejection.Code {
	case "PROVIDER_PROTOCOL_ERROR", "PROVIDER_FORBIDDEN", "PROVIDER_INVALID_ARGUMENT", "PROVIDER_REJECTED":
		return true
	case "AUTH_REQUIRED":
		c.mu.RLock()
		account := c.session.UserID
		c.mu.RUnlock()
		profile, err := c.invoke(ctx, "getUserInfo", object{"user_guid": account}, false, "")
		return err == nil && asObject(profile["user"]).str("user_guid") == account
	}
	return false
}
