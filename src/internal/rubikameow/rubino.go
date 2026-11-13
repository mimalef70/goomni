package rubikameow

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

// defaultRubinoEndpoint is the Rubino (social profile) API used by the reviewed
// m.rubika.ir client: plain JSON, api_version "0", the account auth in the body.
const defaultRubinoEndpoint = "https://rubino1.iranlms.ir/"

const maxRubinoText = 2200

func rubinoClientIdentity() object {
	return object{"app_name": "Main", "app_version": "2.4.6", "package": "m.rubika.ir", "platform": "PWA"}
}

type rubinoRequest struct {
	ProfileID     string   `json:"profile_id"`
	PostID        string   `json:"post_id"`
	PostProfileID string   `json:"post_profile_id"`
	CommentID     string   `json:"comment_id"`
	TargetProfile string   `json:"target_profile_id"`
	StoryID       string   `json:"story_id"`
	StoryIDs      []string `json:"story_ids"`
	StoryProfile  string   `json:"story_profile_id"`
	Text          string   `json:"text"`
	MaxID         string   `json:"max_id"`
	Limit         int64    `json:"limit"`
}

func rubinoOperations() []domains.OperationContract {
	var out []domains.OperationContract
	add := func(name, mode, description string, fields map[string]domains.FieldSchema, required ...string) {
		out = append(out, domains.OperationContract{Operation: name, Mode: mode, Description: description, Request: domains.FieldSchema{Type: "object", Properties: fields, Required: required}})
	}
	id := func() domains.FieldSchema {
		return domains.FieldSchema{Type: "string", MinLength: 1, MaxLength: 64, Pattern: `^[A-Za-z0-9_-]+$`}
	}
	page := func(fields map[string]domains.FieldSchema) map[string]domains.FieldSchema {
		fields["max_id"] = id()
		fields["limit"] = boundedInt(1, 50)
		return fields
	}
	add("rubino.profiles", "read", "List the account's Rubino profiles (pages); the default profile acts for other Rubino operations", map[string]domains.FieldSchema{})
	add("rubino.posts", "read", "List posts of the account's Rubino profile, newest first", page(map[string]domains.FieldSchema{"profile_id": id()}))
	add("rubino.stories", "read", "List the account profile's own active Rubino stories", map[string]domains.FieldSchema{"profile_id": id()})
	add("rubino.story", "read", "Read Rubino stories by opaque IDs; media is exposed only through registered references", map[string]domains.FieldSchema{"profile_id": id(), "story_profile_id": id(), "story_ids": arrayField(1, 10, id())}, "story_profile_id", "story_ids")
	add("rubino.notifications", "read", "Read the account profile's Rubino activity (comments, replies, likes, follows), newest first", page(map[string]domains.FieldSchema{"profile_id": id()}))
	add("rubino.comments", "read", "Read comments on a Rubino post, newest first", page(map[string]domains.FieldSchema{"profile_id": id(), "post_id": id(), "post_profile_id": id()}), "post_id", "post_profile_id")
	add("rubino.comment.replies", "read", "Read replies to one Rubino comment", page(map[string]domains.FieldSchema{"profile_id": id(), "post_id": id(), "post_profile_id": id(), "comment_id": id()}), "post_id", "post_profile_id", "comment_id")
	add("rubino.profile.chat", "read", "Resolve a Rubino profile to its Rubika messenger user peer for a direct reply; fails when the profile does not accept messages", map[string]domains.FieldSchema{"profile_id": id(), "target_profile_id": id()}, "target_profile_id")
	add("rubino.comment.add", "mutation", "Add a comment to a Rubino post from the account profile", map[string]domains.FieldSchema{"profile_id": id(), "post_id": id(), "post_profile_id": id(), "text": {Type: "string", MinLength: 1, MaxLength: maxRubinoText}}, "post_id", "post_profile_id", "text")
	add("rubino.comment.reply", "mutation", "Reply in the thread of one Rubino comment from the account profile", map[string]domains.FieldSchema{"profile_id": id(), "post_id": id(), "post_profile_id": id(), "comment_id": id(), "text": {Type: "string", MinLength: 1, MaxLength: maxRubinoText}}, "post_id", "post_profile_id", "comment_id", "text")
	return out
}

func isRubinoOperation(name string) bool { return strings.HasPrefix(name, "rubino.") }

// rubino performs one Rubino API call with the account session.
func (c *Client) rubino(ctx context.Context, method string, data object, mutating bool) (object, error) {
	select {
	case c.rpcSlots <- struct{}{}:
		defer func() { <-c.rpcSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.mu.RLock()
	auth := c.session.Auth
	c.mu.RUnlock()
	if auth == "" {
		return nil, domains.E("AUTH_REQUIRED", "account authentication is required", 401)
	}
	endpoint := c.cfg.RubinoEndpoint
	if endpoint == "" {
		endpoint = defaultRubinoEndpoint
	}
	if !validEndpoint(endpoint, false, c.cfg.HTTPClient != nil) {
		return nil, protocolError()
	}
	response, err := c.postJSON(ctx, endpoint, object{"api_version": "0", "auth": auth, "client": rubinoClientIdentity(), "method": method, "data": data}, mutating)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(response.str("status"), "OK") {
		failure := rpcError(response.str("status_det"))
		var de *domains.Error
		if response.str("status") == "" || (asDomainError(failure, &de) && de.Code == "PROVIDER_REJECTED") {
			return nil, &domains.Error{Code: "PROVIDER_PROTOCOL_ERROR", Message: "Rubino returned an unsupported response status", HTTP: 502, Ambiguous: mutating}
		}
		return nil, failure
	}
	result := asObject(response["data"])
	if result == nil {
		return nil, &domains.Error{Code: "PROVIDER_PROTOCOL_ERROR", Message: "Rubino returned an unsupported response body", HTTP: 502, Ambiguous: mutating}
	}
	return result, nil
}

// defaultRubinoProfile resolves the account's default Rubino profile.
func (c *Client) defaultRubinoProfile(ctx context.Context) (string, error) {
	data, err := c.rubino(ctx, "getProfileList", object{"equal": false, "limit": 10, "sort": "FromMax"}, false)
	if err != nil {
		return "", err
	}
	profiles := asObjects(data["profiles"])
	for _, p := range profiles {
		if b, _ := p["is_default"].(bool); b && validRubinoID(p.str("id")) {
			return p.str("id"), nil
		}
	}
	if len(profiles) > 0 && validRubinoID(profiles[0].str("id")) {
		return profiles[0].str("id"), nil
	}
	return "", domains.E("RUBINO_PROFILE_REQUIRED", "the account has no Rubino profile", 409)
}

func validRubinoID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func (c *Client) rubinoCall(ctx context.Context, name string, raw json.RawMessage, rid string) (json.RawMessage, bool, error) {
	if !isRubinoOperation(name) {
		return nil, false, nil
	}
	var p rubinoRequest
	if json.Unmarshal(raw, &p) != nil {
		return nil, true, domains.E("INVALID_REQUEST", "invalid operation payload", 400)
	}
	finish := func(o object, err error) (json.RawMessage, bool, error) {
		if err != nil {
			return nil, true, err
		}
		b, _ := json.Marshal(o)
		return b, true, nil
	}
	if name == "rubino.profiles" {
		data, err := c.rubino(ctx, "getProfileList", object{"equal": false, "limit": 10, "sort": "FromMax"}, false)
		if err != nil {
			return finish(nil, err)
		}
		var profiles []object
		for _, profile := range asObjects(data["profiles"]) {
			profiles = append(profiles, projectRubinoProfile(profile))
		}
		return finish(object{"profiles": nonNil(profiles)}, nil)
	}
	profile := p.ProfileID
	if profile == "" {
		var err error
		if profile, err = c.defaultRubinoProfile(ctx); err != nil {
			return finish(nil, err)
		}
	}
	limit := p.Limit
	if limit == 0 {
		limit = 20
	}
	paged := func(data object) object {
		data["limit"] = limit
		data["sort"] = "FromMax"
		if p.MaxID != "" {
			data["max_id"] = p.MaxID
		}
		return data
	}
	switch name {
	case "rubino.posts":
		data, err := c.rubino(ctx, "getMyProfilePosts", paged(object{"profile_id": profile}), false)
		if err != nil {
			return finish(nil, err)
		}
		var posts []object
		for _, post := range asObjects(data["posts"]) {
			posts = append(posts, projectRubinoPost(post))
		}
		return finish(object{"profile_id": profile, "posts": nonNil(posts), "next_max_id": nextRubinoMax(posts)}, nil)
	case "rubino.stories":
		data, err := c.rubino(ctx, "getMyStoriesList", object{"profile_id": profile}, false)
		if err != nil {
			return finish(nil, err)
		}
		var stories []object
		for _, story := range asObjects(data["stories"]) {
			stories = append(stories, projectRubinoStoryRecord(story))
		}
		return finish(object{"profile_id": profile, "stories": nonNil(stories)}, nil)
	case "rubino.story":
		data, err := c.rubino(ctx, "getStory", object{"profile_id": profile, "story_profile_id": p.StoryProfile, "story_ids": p.StoryIDs}, false)
		if err != nil {
			return finish(nil, err)
		}
		var stories []object
		for _, story := range asObjects(data["stories"]) {
			stories = append(stories, projectRubinoStoryRecord(story))
		}
		return finish(object{"stories": nonNil(stories)}, nil)
	case "rubino.notifications":
		data, err := c.rubino(ctx, "getNewEvents", paged(object{"profile_id": profile}), false)
		if err != nil {
			return finish(nil, err)
		}
		var events []object
		for _, record := range asObjects(data["records"]) {
			events = append(events, projectRubinoEvent(record))
		}
		return finish(object{"profile_id": profile, "events": nonNil(events), "next_max_id": nextRubinoMax(events)}, nil)
	case "rubino.comments":
		data, err := c.rubino(ctx, "getComments", paged(object{"profile_id": profile, "post_id": p.PostID, "post_profile_id": p.PostProfileID}), false)
		if err != nil {
			return finish(nil, err)
		}
		var comments []object
		for _, comment := range asObjects(data["comments"]) {
			comments = append(comments, projectRubinoComment(comment))
		}
		return finish(object{"comments": nonNil(comments), "next_max_id": nextRubinoMax(comments)}, nil)
	case "rubino.comment.replies":
		data, err := c.rubino(ctx, "getCommentReplies", paged(object{"profile_id": profile, "post_id": p.PostID, "post_profile_id": p.PostProfileID, "comment_id": p.CommentID}), false)
		if err != nil {
			return finish(nil, err)
		}
		var comments []object
		for _, comment := range asObjects(data["comments"]) {
			comments = append(comments, projectRubinoComment(comment))
		}
		return finish(object{"comments": nonNil(comments), "next_max_id": nextRubinoMax(comments)}, nil)
	case "rubino.profile.chat":
		peer, err := c.rubinoProfileChat(ctx, profile, p.TargetProfile)
		if err != nil {
			return finish(nil, err)
		}
		return finish(object{"peer": peer}, nil)
	case "rubino.comment.add", "rubino.comment.reply":
		rnd, err := strconv.ParseInt(rid, 10, 64)
		if err != nil || rnd <= 0 {
			return finish(nil, domains.E("INVALID_REQUEST_ID", "persist a positive request ID before mutation", 400))
		}
		data := object{"content": p.Text, "post_id": p.PostID, "post_profile_id": p.PostProfileID, "profile_id": profile, "rnd": rnd}
		method := "addComment"
		if name == "rubino.comment.reply" {
			method = "addReplyComment"
			data["comment_id"] = p.CommentID
		}
		result, err := c.rubino(ctx, method, data, true)
		if err != nil {
			return finish(nil, err)
		}
		comment := projectRubinoComment(asObject(result["comment"]))
		return finish(object{"acknowledged": true, "comment": comment}, nil)
	}
	return finish(nil, domains.Unsupported("unknown Rubino operation"))
}

func normalizeRubino(name string, raw json.RawMessage) error {
	if !isRubinoOperation(name) {
		return nil
	}
	var p rubinoRequest
	if json.Unmarshal(raw, &p) != nil {
		return domains.E("INVALID_REQUEST", "invalid operation payload", 400)
	}
	for _, id := range append([]string{p.ProfileID, p.PostID, p.PostProfileID, p.CommentID, p.TargetProfile, p.StoryProfile, p.MaxID}, p.StoryIDs...) {
		if id != "" && !validRubinoID(id) {
			return domains.E("INVALID_REQUEST", "Rubino identifiers are bounded opaque tokens", 400)
		}
	}
	seen := map[string]bool{}
	for _, id := range p.StoryIDs {
		if seen[id] {
			return domains.E("INVALID_REQUEST", "story IDs must be unique", 400)
		}
		seen[id] = true
	}
	if (name == "rubino.comment.add" || name == "rubino.comment.reply") && strings.TrimSpace(p.Text) == "" {
		return domains.E("INVALID_REQUEST", "comment text must not be blank", 400)
	}
	return nil
}

func nonNil(v []object) []object {
	if v == nil {
		return []object{}
	}
	return v
}

func nextRubinoMax(items []object) any {
	if len(items) == 0 {
		return nil
	}
	if id, ok := items[len(items)-1]["id"].(string); ok && id != "" {
		return id
	}
	return nil
}

// copyStrings copies reviewed string fields bounded to n bytes.
func copyStrings(dst, src object, n int, keys ...string) {
	for _, key := range keys {
		if v, ok := src[key].(string); ok && len(v) <= n {
			dst[key] = v
		} else if num, ok := src[key].(json.Number); ok {
			dst[key] = num.String()
		}
	}
}

func copyScalars(dst, src object, keys ...string) {
	for _, key := range keys {
		switch v := src[key].(type) {
		case bool:
			dst[key] = v
		case json.Number:
			dst[key] = v.String()
		}
	}
}

func projectRubinoProfile(p object) object {
	out := object{}
	copyStrings(out, p, 256, "id", "username", "name", "bio", "profile_type", "profile_status")
	copyScalars(out, p, "is_default", "is_verified", "is_message_allowed", "post_count", "follower_count", "following_count")
	return out
}

func projectRubinoPost(p object) object {
	out := object{}
	copyStrings(out, p, maxRubinoText, "id", "profile_id", "post_type", "file_type", "caption")
	copyScalars(out, p, "create_date", "comment_count", "like_count", "view_count")
	return out
}

func projectRubinoComment(p object) object {
	out := object{}
	if p == nil {
		return out
	}
	copyStrings(out, p, maxRubinoText, "id", "post_id", "post_profile_id", "profile_id", "profile_username", "content", "parent_id")
	copyScalars(out, p, "create_date", "reply_count", "likes_count")
	if profile := asObject(p["profile"]); profile != nil {
		out["profile"] = projectRubinoProfile(profile)
	}
	return out
}

func projectRubinoEvent(p object) object {
	out := object{}
	copyStrings(out, p, maxRubinoText, "id", "model", "post_id", "post_profile_id", "comment_id", "story_id", "profile_id")
	copyScalars(out, p, "create_date", "is_new")
	var owners []object
	for _, owner := range asObjects(p["owners"]) {
		if len(owners) == 10 {
			break
		}
		owners = append(owners, projectRubinoProfile(owner))
	}
	if owners != nil {
		out["owners"] = owners
	}
	for _, key := range []string{"comment", "post"} {
		if o := asObject(p[key]); o != nil {
			switch key {
			case "comment":
				out[key] = projectRubinoComment(o)
			default:
				out[key] = projectRubinoPost(o)
			}
		}
	}
	return out
}

func projectRubinoStoryRecord(p object) object {
	out := object{}
	copyStrings(out, p, 256, "id", "profile_id", "story_type")
	copyScalars(out, p, "create_date", "duration", "viewer_count")
	_, hasFile := p["full_file_url"].(string)
	_, hasSnapshot := p["snapshot_url"].(string)
	out["media_available"] = hasFile || hasSnapshot
	return out
}

func asDomainError(err error, target **domains.Error) bool { return errors.As(err, target) }

// rubinoProfileChat maps a Rubino profile to its messenger user through the
// profile's public username, as the reviewed client's message button does.
func (c *Client) rubinoProfileChat(ctx context.Context, viewer, target string) (domains.Peer, error) {
	data, err := c.rubino(ctx, "getProfileInfo", object{"profile_id": viewer, "target_profile_id": target}, false)
	if err != nil {
		return domains.Peer{}, err
	}
	profile := asObject(data["profile"])
	username := profile.str("username")
	if profile == nil || profile.str("id") != target || username == "" || len(username) > 64 {
		return domains.Peer{}, domains.E("RUBINO_PROFILE_NOT_FOUND", "Rubino profile was not found", 404)
	}
	if allowed, ok := profile["is_message_allowed"].(bool); ok && !allowed {
		return domains.Peer{}, domains.E("RUBINO_MESSAGES_NOT_ALLOWED", "the Rubino profile does not accept direct messages", 409)
	}
	if open := asObject(asObject(profile["chat_link"])["open_chat_data"]); open.str("object_type") == "User" && (protocolRules{}).ValidateUserID(open.str("object_guid")) {
		return domains.Peer{Type: "user", ID: open.str("object_guid")}, nil
	}
	info, err := c.invoke(ctx, "getObjectInfoByUsername", object{"username": username}, false, "")
	if err != nil {
		return domains.Peer{}, err
	}
	user := asObject(info["user"])
	guid := user.str("user_guid")
	if exist, _ := info["exist"].(bool); !exist || !(protocolRules{}).ValidateUserID(guid) {
		return domains.Peer{}, domains.E("RUBINO_MESSAGES_NOT_ALLOWED", "the Rubino profile has no reachable messenger user", 409)
	}
	return domains.Peer{Type: "user", ID: guid}, nil
}
