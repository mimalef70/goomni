package rubikameow

import (
	"context"
	"github.com/mimalef70/goomni/src/domains"
	"testing"
)

func TestRecoveryAuthRejectionRequiresSelfIdentityProof(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response object
		lost     bool
	}{
		{"peer-only rejection", object{"user": object{"user_guid": "u0self"}}, false},
		{"self rejection", object{"_fixture_error": object{"status": "ERROR", "status_det": "INVALID_AUTH"}}, true},
		{"self changed", object{"user": object{"user_guid": "u0other"}}, true},
		{"incomplete profile", object{}, false},
		{"temporary HTTP failure", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c, _ := newRPCFixture(t, func(method string, input object, anonymous bool) object {
				calls++
				if method != "getUserInfo" || input.str("user_guid") != "u0self" || anonymous {
					t.Fatal("validation escaped authenticated self scope")
				}
				return tc.response
			})
			if c.recoveryAuthRequired(context.Background(), domains.E("PROVIDER_PROTOCOL_ERROR", "synthetic", 502)) || calls != 0 {
				t.Fatal("non-auth error triggered identity lookup")
			}
			if got := c.recoveryAuthRequired(context.Background(), domains.E("AUTH_REQUIRED", "synthetic peer rejection", 401)); got != tc.lost || calls != 1 {
				t.Fatalf("authentication loss=%v want=%v calls=%d", got, tc.lost, calls)
			}
		})
	}
}
