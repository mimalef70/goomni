package usecase

import (
	"context"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

func (s *WebhookService) WebhookDetails(ctx context.Context, id string) (domains.WebhookDetails, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.WebhookDetails{}, err
	}
	result := domains.WebhookDetails{WebhookConfig: d.Webhook, SecretConfigured: d.Webhook.Secret != "", RoutingMode: "none", RoutingRules: []domains.WebhookRoutingRule{}}
	if d.Webhook.URL != "" {
		result.RoutingMode = "device"
		result.RoutingRules = append(result.RoutingRules, domains.WebhookRoutingRule{Source: "device", URL: d.Webhook.URL, Events: append([]string{}, d.Webhook.Events...), Filter: d.Webhook.Filter, SecretConfigured: d.Webhook.Secret != ""})
	}
	if d.Webhook.URL == "" || s.options.MergeGlobal {
		for _, target := range s.options.GlobalWebhooks {
			result.RoutingRules = append(result.RoutingRules, domains.WebhookRoutingRule{Source: "global", URL: target.URL, Events: append([]string{}, s.options.GlobalWebhookEvents...), SecretConfigured: target.Secret != ""})
		}
		if len(s.options.GlobalWebhooks) > 0 {
			if d.Webhook.URL == "" {
				result.RoutingMode = "global"
			} else {
				result.RoutingMode = "merged"
			}
		}
	}
	return result, nil
}

func (s *WebhookService) ListDeliveriesFiltered(ctx context.Context, id string, limit, offset int, state string, includePayload bool) ([]domains.Delivery, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, offset = page(limit, offset)
	return s.webhookStore.ListDeliveriesFiltered(ctx, d.ConnectionID, limit, offset, state, includePayload)
}

func (s *WebhookService) GetWebhook(ctx context.Context, id string) (domains.WebhookConfig, error) {
	d, err := s.ResolveDevice(ctx, id)
	return d.Webhook, err
}

func (s *WebhookService) PatchWebhook(ctx context.Context, id string, patch domains.WebhookPatch) (domains.WebhookConfig, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.WebhookConfig{}, err
	}
	if patch.URL != nil && *patch.URL != "" {
		if err = validateWebhookURL(*patch.URL); err != nil {
			return domains.WebhookConfig{}, err
		}
	}
	effectiveURL, effectiveSecret := d.Webhook.URL, d.Webhook.Secret
	if patch.URL != nil {
		effectiveURL = *patch.URL
	}
	if patch.Secret != nil {
		effectiveSecret = *patch.Secret
	}
	if effectiveURL != "" && strings.TrimSpace(effectiveSecret) == "" {
		return domains.WebhookConfig{}, domains.E("WEBHOOK_SECRET_REQUIRED", "configure a non-empty secret before enabling a device webhook", 400)
	}
	if patch.Secret != nil && len(*patch.Secret) > 4096 {
		return domains.WebhookConfig{}, domains.E("INVALID_WEBHOOK", "webhook secret exceeds 4096 bytes", 400)
	}
	if patch.Events != nil {
		if len(*patch.Events) > 100 {
			return domains.WebhookConfig{}, domains.E("INVALID_WEBHOOK", "too many event filters", 400)
		}
		for _, name := range *patch.Events {
			if strings.TrimSpace(name) == "" || len(name) > 128 {
				return domains.WebhookConfig{}, domains.E("INVALID_WEBHOOK", "invalid event filter", 400)
			}
		}
	}
	if patch.Filter != nil {
		contract, err := s.provider(d.Provider)
		if err != nil {
			return domains.WebhookConfig{}, err
		}
		if err = validateFilter(contract, *patch.Filter); err != nil {
			return domains.WebhookConfig{}, err
		}
	}
	return s.webhookStore.PatchWebhook(ctx, d.ConnectionID, patch)
}

func (s *WebhookService) ListDeliveries(ctx context.Context, id string, limit, offset int) ([]domains.Delivery, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, offset = page(limit, offset)
	return s.webhookStore.ListDeliveries(ctx, d.ConnectionID, limit, offset)
}

func (s *WebhookService) GetDelivery(ctx context.Context, id, deliveryID string) (domains.Delivery, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Delivery{}, err
	}
	return s.webhookStore.GetDelivery(ctx, d.ConnectionID, deliveryID)
}

func (s *WebhookService) ReplayDelivery(ctx context.Context, id, deliveryID string) ([]domains.Delivery, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	delivery, err := s.webhookStore.GetDelivery(ctx, d.ConnectionID, deliveryID)
	if err != nil {
		return nil, err
	}
	eventType := eventTypeFromBody(delivery.Body)
	targets := s.targets(d, eventType)
	if len(targets) == 0 {
		return nil, domains.E("NO_WEBHOOK_TARGETS", "no active webhook target accepts this event", 409)
	}
	return s.webhookStore.ReplayDelivery(ctx, d.ConnectionID, deliveryID, targets)
}

func (s *WebhookService) RetryDelivery(ctx context.Context, id, deliveryID string) error {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	_, err = s.webhookStore.RetryDelivery(ctx, d.ConnectionID, deliveryID)
	return err
}
