package usecase

import (
	"errors"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/sirupsen/logrus"
)

// Called under the connection lock. Retain one successful direct send as a
// comparison for later failures, without writing success logs or recipient IDs.
func (e *clientEntry) observeTextSendAttempt(provider domains.Provider, op domains.Operation, diagnostic *domains.TextSendDiagnostic, err error) {
	direct := provider == domains.ProviderBale && op.Request.Peer.Type == "user"
	if err == nil {
		if direct && diagnostic.PeerHash != "" {
			e.lastBaleDMOperation, e.lastBaleDMPeerHash = op.ID, diagnostic.PeerHash
		}
		return
	}
	fields := logrus.Fields{"provider": provider, "operation_id": op.ID, "stage": diagnostic.Stage}
	if diagnostic.HTTPStatus != 0 {
		fields["http_status"] = diagnostic.HTTPStatus
	}
	if diagnostic.ProviderCode != 0 {
		fields["provider_code"] = diagnostic.ProviderCode
	}
	if diagnostic.Reason != "" {
		fields["reason"] = diagnostic.Reason
	}
	if diagnostic.Renewal != "" {
		fields["renewal"] = diagnostic.Renewal
	}
	if diagnostic.PeerHash != "" {
		fields["peer_hash"] = diagnostic.PeerHash
	}
	if diagnostic.RejectedField != "" {
		fields["rejected_field"] = diagnostic.RejectedField
	}
	var sendErr *domains.Error
	if errors.As(err, &sendErr) {
		fields["adapter_code"] = safeCode(sendErr.Code)
	}
	if direct && e.lastBaleDMOperation != "" {
		fields["previous_success_operation_id"] = e.lastBaleDMOperation
		fields["previous_success_peer_hash"] = e.lastBaleDMPeerHash
	}
	logrus.WithFields(fields).Warn("Text send attempt failed")
}
