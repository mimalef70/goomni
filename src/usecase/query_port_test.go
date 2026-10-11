package usecase

import (
	"context"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

type queryAccounts struct{ AccountStore }

func (queryAccounts) GetDevice(_ context.Context, id string) (domains.Device, error) {
	if id != "selected" {
		return domains.Device{}, domains.E("NOT_FOUND", "not found", 404)
	}
	return domains.Device{ID: id, ConnectionID: "immutable-selected", Provider: domains.ProviderBale}, nil
}

type queryEvents struct {
	EventStore
	connection string
	calls      int
}

func (p *queryEvents) ListChats(_ context.Context, connection string, _, _ int) ([]domains.Chat, error) {
	p.connection = connection
	p.calls++
	return []domains.Chat{}, nil
}
func TestQueryServiceUsesScopedPortsWithoutSQLite(t *testing.T) {
	events := &queryEvents{}
	s := &QueryService{serviceRuntime: &serviceRuntime{accountStore: queryAccounts{}}, eventStore: events}
	_, err := s.Chats(context.Background(), "selected", 50, 0)
	require.NoError(t, err)
	require.Equal(t, "immutable-selected", events.connection)
	_, err = s.Chats(context.Background(), "missing", 50, 0)
	require.Error(t, err)
	require.Equal(t, 1, events.calls, "invalid selectors must not fall back or reach the read port")
}
