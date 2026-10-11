package rest

import (
	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/goomni/src/domains"
)

func (s *Server) capabilities(c fiber.Ctx) error {
	provider := domains.Provider(c.Query("provider"))
	if err := provider.Validate(); err != nil {
		return err
	}
	list, err := s.service.Capabilities(provider)
	return result(c, list, err)
}
