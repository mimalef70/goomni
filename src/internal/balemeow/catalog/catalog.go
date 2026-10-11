// Package catalog contains reviewed Bale operation schemas and semantics.
package catalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	"github.com/mimalef70/goomni/src/domains"
)

// Operation contracts are shared by admission, discovery and OpenAPI generation.
// Adding a contract does not permit arbitrary RPC names or raw provider payloads.
//
//go:embed operation_catalog.json
var operationCatalog []byte

var operationContracts = func() map[string]domains.OperationContract {
	var list []domains.OperationContract
	if err := json.Unmarshal(operationCatalog, &list); err != nil {
		panic(err)
	}
	m := make(map[string]domains.OperationContract, len(list))
	for _, c := range list {
		if c.Operation == "" || c.Request.Type != "object" || (c.Mode != "read" && c.Mode != "mutation" && c.Mode != "ephemeral") {
			panic("invalid operation catalog entry")
		}
		if _, ok := m[c.Operation]; ok {
			panic("duplicate operation contract")
		}
		m[c.Operation] = c
	}
	return m
}()

func OperationDefinition(name string) (domains.OperationContract, bool) {
	c, ok := operationContracts[name]
	return c, ok
}
func OperationDefinitions() []domains.OperationContract {
	list := make([]domains.OperationContract, 0, len(operationContracts))
	for _, c := range operationContracts {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Operation < list[j].Operation })
	return list
}
func IsExtendedMutation(name string) bool {
	c, ok := operationContracts[name]
	return ok && c.Mode == "mutation"
}

// NormalizeOperation rejects unknown/nested private fields before durable enqueue.
// IDs remain strings, including integers above JavaScript's exact range.
func NormalizeOperation(name string, raw json.RawMessage) (json.RawMessage, domains.Peer, error) {
	c, ok := operationContracts[name]
	if !ok {
		return nil, domains.Peer{}, domains.Unsupported(name)
	}
	normalized, peer, err := domains.NormalizeOperationContract(c, raw)
	if err != nil {
		return nil, peer, err
	}
	var fields map[string]any
	d := json.NewDecoder(bytes.NewReader(normalized))
	d.UseNumber()
	if err := d.Decode(&fields); err != nil {
		return nil, peer, err
	}
	if name == "account.settings.set" {
		key, _ := fields["key"].(string)
		for _, term := range []string{"password", "token", "secret", "credential", "cookie", "auth", "access_hash", "api_key"} {
			if strings.Contains(strings.ToLower(key), term) {
				return nil, domains.Peer{}, domains.E("INVALID_REQUEST", "sensitive authentication settings are not accepted by this endpoint", 400)
			}
		}
	}
	if name == "group.permissions.set" || name == "group.default_permissions.set" {
		permissions := fields["permissions"].(map[string]any)
		if len(permissions) == 0 {
			return nil, domains.Peer{}, domains.E("INVALID_REQUEST", "permission patch must not be empty", 400)
		}
		if fields["mode"] == "replace" && len(permissions) != 20 {
			return nil, domains.Peer{}, domains.E("INVALID_REQUEST", "permission replacement requires all 20 explicit booleans", 400)
		}
	}
	if name == "story.add" {
		_, text := fields["text"]
		_, media := fields["media_id"]
		if text == media {
			return nil, domains.Peer{}, domains.E("INVALID_REQUEST", "provide exactly one of text or media_id", 400)
		}
	}
	if name == "report.story" {
		seen := map[string]bool{}
		for _, value := range fields["story_ids"].([]any) {
			id := value.(string)
			if len(id) > 512 || strings.TrimSpace(id) == "" || strings.IndexFunc(id, unicode.IsControl) >= 0 || seen[id] {
				return nil, domains.Peer{}, domains.E("INVALID_REQUEST", "story_ids requires distinct opaque IDs of at most 512 bytes without control characters", 400)
			}
			seen[id] = true
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, domains.Peer{}, err
	}
	var p struct {
		Peer domains.Peer `json:"peer"`
	}
	_ = json.Unmarshal(encoded, &p)
	return encoded, p.Peer, nil
}
