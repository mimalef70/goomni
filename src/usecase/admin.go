package usecase

import (
	"time"
	"unicode"

	"github.com/mimalef70/goomni/src/domains"
)

func (e *clientEntry) adminStatus(now time.Time) domains.ConnectionStatus {
	status, _ := e.adminSnapshot(now)
	return status
}

func (e *clientEntry) adminSnapshot(now time.Time) (domains.ConnectionStatus, *domains.PublicChallenge) {
	// Copy once so a refresh never advertises an awaiting state with no usable
	// challenge. This read does not wait for the lifecycle/provider operation lock.
	challenge := e.publicChallenge(now)
	e.clientMu.RLock()
	client := e.client
	e.clientMu.RUnlock()
	status := cleanStatus(client.Status())
	// Expired provider challenges can remain in a client's status until another
	// authentication call. Never advertise them as usable in a refreshed form.
	if status.Auth == "awaiting_code" || status.Auth == "awaiting_password" {
		if challenge == nil {
			status.Auth = "auth_required"
		}
	} else {
		challenge = nil
	}
	return status, challenge
}

func (e *clientEntry) publicChallenge(now time.Time) *domains.PublicChallenge {
	e.authMetaMu.Lock()
	defer e.authMetaMu.Unlock()
	if e.challenge != nil && !now.Before(e.challenge.ExpiresAt) {
		e.challenge = nil
	}
	if e.challenge == nil {
		return nil
	}
	copy := *e.challenge
	copy.AvailableDeliveries = append([]string{}, copy.AvailableDeliveries...)
	copy.AvailableSendCodeTypes = append([]int32{}, copy.AvailableSendCodeTypes...)
	return &copy
}

func (e *clientEntry) clearAuthMetadata() {
	e.authMetaMu.Lock()
	e.challenge = nil
	e.resendAvailableAt = time.Time{}
	e.authMetaMu.Unlock()
}

func maskedPhone(phone string) string {
	// Keep only the last two digits. No raw caller text can become UI markup or
	// reveal an international prefix with a short input.
	var digits []rune
	for _, r := range phone {
		if unicode.IsDigit(r) {
			digits = append(digits, r)
		}
	}
	if len(digits) < 3 {
		return "••••"
	}
	return "••••••••" + string(digits[len(digits)-2:])
}
