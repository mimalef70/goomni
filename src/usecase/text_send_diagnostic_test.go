package usecase

import (
	"bytes"
	"context"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestTextSendDiagnosticWorkerCorrelatesOnlyWithinConnection(t *testing.T) {
	logger := logrus.StandardLogger()
	previous := logger.Out
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	t.Cleanup(func() { logger.SetOutput(previous) })

	f := &fakeClient{status: readyStatus()}
	f.sendFn = func(ctx context.Context, req domains.SendRequest) (domains.SendResult, error) {
		d := domains.TextSendDiagnosticFrom(ctx)
		require.NotNil(t, d)
		if req.Text == "private-success" {
			d.PeerHash = "present"
			return domains.SendResult{MessageID: "99"}, nil
		}
		d.Stage, d.PeerHash, d.ProviderCode = "provider_error", "absent", 3
		return domains.SendResult{}, domains.E("PROVIDER_INVALID_ARGUMENT", "private-provider-message", 400)
	}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	a, b := mustDevice(t, s, "alpha"), mustDevice(t, s, "beta")
	send := func(dev domains.Device, content string) string {
		op, err := s.Send(context.Background(), dev.ID, sendReq(content), content)
		require.NoError(t, err)
		s.processOperation(claimOne(t, st))
		return op.ID
	}
	control := send(a, "private-success")
	require.Empty(t, buf.String())
	_ = send(b, "private-other-account-content")
	require.NotContains(t, buf.String(), control)
	require.NotContains(t, buf.String(), "previous_success")
	buf.Reset()
	failed := send(a, "private-message-content")
	for _, want := range []string{failed, control, "previous_success_peer_hash=present", "peer_hash=absent", "provider_code=3"} {
		require.Contains(t, buf.String(), want)
	}
	require.NotContains(t, buf.String(), "private-")
}
