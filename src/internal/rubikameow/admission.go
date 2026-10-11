package rubikameow

import (
	"encoding/json"

	"github.com/mimalef70/goomni/src/domains"
)

// Admission helpers share protocol validation with the gateway adapter without
// importing its HTTP catalogue, persistence, or runtime configuration.
func ValidateUserID(id string) bool            { return (protocolRules{}).ValidateUserID(id) }
func SupportsPeerType(kind string) bool        { return (protocolRules{}).SupportsPeerType(kind) }
func ValidateSenderID(id string) bool          { return (protocolRules{}).ValidateSenderID(id) }
func ValidatePeer(p domains.Peer) error        { return (protocolRules{}).ValidatePeer(p) }
func ValidateSend(r domains.SendRequest) error { return (protocolRules{}).ValidateSend(r) }
func ValidateSession(s *domains.Session) error { return (protocolRules{}).ValidateSession(s) }
func Operations() []domains.OperationContract  { return (protocolRules{}).Operations() }
func NormalizeOperation(name string, raw json.RawMessage) (json.RawMessage, domains.Peer, error) {
	return (protocolRules{}).NormalizeOperation(name, raw)
}
