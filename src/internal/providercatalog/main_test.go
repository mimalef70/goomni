package main

import (
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/infrastructure/providers/eitaa"
	"github.com/mimalef70/goomni/src/infrastructure/providers/rubika"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
)

func TestProviderCatalogsHaveFiniteCompleteContracts(t *testing.T) {
	for _, c := range []domains.ProviderContract{bale.Contract{}, eitaa.Contract{}, rubika.Contract{}} {
		t.Run(string(c.Descriptor().ID), func(t *testing.T) {
			seen := map[string]bool{}
			for _, op := range c.Operations() {
				if seen[op.Operation] || op.Operation == "" || op.Request.Type != "object" || op.Description == "" || op.Verification == "" || op.Evidence.Level == "" || op.Evidence.Scope == "" || len(op.Evidence.References) == 0 {
					t.Fatalf("incomplete or repeated operation %q", op.Operation)
				}
				seen[op.Operation] = true
				if op.Method != "GET" && op.Method != "POST" {
					t.Fatalf("missing HTTP method: %s", op.Operation)
				}
				if !strings.HasPrefix(op.Path, "/") {
					t.Fatalf("missing HTTP route: %s", op.Operation)
				}
				if op.Mode != "read" && op.Mode != "mutation" && op.Mode != "ephemeral" {
					t.Fatalf("invalid mode: %s", op.Operation)
				}
				if op.Schedulable && op.Mode != "mutation" {
					t.Fatalf("non-mutation scheduled: %s", op.Operation)
				}
				var check func(domains.FieldSchema)
				check = func(s domains.FieldSchema) {
					for _, name := range s.Required {
						if _, ok := s.Properties[name]; !ok {
							t.Errorf("%s requires nonexistent field %s", op.Operation, name)
						}
					}
					for _, child := range s.Properties {
						check(child)
					}
					if s.Items != nil {
						check(*s.Items)
					}
				}
				check(op.Request)
			}
			if _, _, err := c.NormalizeOperation("arbitrary.provider.rpc", []byte(`{}`)); err == nil {
				t.Fatal("unregistered RPC was admitted")
			}
		})
	}
}
