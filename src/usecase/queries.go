package usecase

import (
	"context"

	"github.com/mimalef70/goomni/src/domains"
)

func (s *QueryService) EventsFiltered(ctx context.Context, id string, f domains.EventFilter) ([]domains.Event, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = s.validateQueryIdentity(d, f.Peer, f.SenderID); err != nil {
		return nil, err
	}
	f.Limit, f.Offset = page(f.Limit, f.Offset)
	return s.eventStore.ListEventsFiltered(ctx, d.ConnectionID, f)
}

func (s *QueryService) Chats(ctx context.Context, id string, limit, offset int) ([]domains.Chat, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, offset = page(limit, offset)
	return s.eventStore.ListChats(ctx, d.ConnectionID, limit, offset)
}

func (s *QueryService) Messages(ctx context.Context, id, peer string, limit, offset int) ([]domains.Event, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, offset = page(limit, offset)
	return s.eventStore.ListEvents(ctx, d.ConnectionID, peer, limit, offset)
}
