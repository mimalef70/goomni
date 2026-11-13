package rubikameow

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

// storyMediaVersion marks a private reference to a Rubino story image URL.
// Messenger file references keep privateMedia.Version 1.
const storyMediaVersion = 2

const storyMediaTimeout = 10 * time.Second

type privateStoryMedia struct {
	Version int    `json:"version"`
	Account string `json:"account"`
	URL     string `json:"url"`
	Size    int64  `json:"size"`
}

// withStoryMedia attaches the referenced story's image to story-reply events
// before they are persisted. Attachment is best effort: an expired story, a
// Rubino failure or an oversized image leaves the event without media and never
// blocks or fails the batch.
func (c *Client) withStoryMedia(next domains.BatchSink) domains.BatchSink {
	return func(ctx context.Context, batch domains.EventBatch) error {
		for i := range batch.Events {
			c.attachStoryMedia(ctx, &batch.Events[i])
		}
		return next(ctx, batch)
	}
}

func (c *Client) attachStoryMedia(ctx context.Context, event *domains.Event) {
	if event.Message != nil && event.Message.Kind == "rubino_post" && event.Media == nil {
		c.attachPostMedia(ctx, event)
		return
	}
	if event.Message == nil || event.Message.Kind != "story_reply" || event.Media != nil {
		return
	}
	var payload struct {
		Story struct {
			StoryID string `json:"story_id"`
			Profile string `json:"story_profile_id"`
		} `json:"rubino_story"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil || !validRubinoID(payload.Story.StoryID) || !validRubinoID(payload.Story.Profile) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, storyMediaTimeout)
	defer cancel()
	viewer, err := c.cachedRubinoProfile(ctx)
	if err != nil {
		return
	}
	data, err := c.rubino(ctx, "getStory", object{"profile_id": viewer, "story_profile_id": payload.Story.Profile, "story_ids": []string{payload.Story.StoryID}}, false)
	if err != nil {
		return
	}
	stories := asObjects(data["stories"])
	if len(stories) != 1 || stories[0].str("id") != payload.Story.StoryID {
		return
	}
	link := c.storyImageURL(stories[0])
	if link == "" {
		return
	}
	size, contentType, ok := c.probeStoryImage(ctx, link)
	if !ok {
		return
	}
	c.setRubinoImage(event, payload.Story.StoryID, "rubino-story", link, size, contentType)
}

func (c *Client) setRubinoImage(event *domains.Event, id, base, link string, size int64, contentType string) {
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	private, _ := json.Marshal(privateStoryMedia{Version: storyMediaVersion, Account: account, URL: link, Size: size})
	name := base + extensionFor(contentType)
	event.Media = &domains.ProviderMedia{Provider: domains.ProviderRubika, Version: 1, Data: private, FileID: id, Size: size, Name: name, ContentType: contentType}
	event.Message.Media = &domains.MessageMedia{Type: "image", FileID: id, Size: size, Name: name, MIMEType: contentType}
}

// attachPostMedia adds a forwarded post's caption and image, best effort.
func (c *Client) attachPostMedia(ctx context.Context, event *domains.Event) {
	var payload map[string]any
	if json.Unmarshal(event.Payload, &payload) != nil {
		return
	}
	ref, _ := payload["rubino_post"].(map[string]any)
	postID, _ := ref["post_id"].(string)
	owner, _ := ref["post_profile_id"].(string)
	if !validRubinoID(postID) || !validRubinoID(owner) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, storyMediaTimeout)
	defer cancel()
	viewer, err := c.cachedRubinoProfile(ctx)
	if err != nil {
		return
	}
	data, err := c.rubino(ctx, "getProfilePosts", object{"profile_id": viewer, "target_profile_id": owner, "max_id": postID, "min_id": postID, "equal": true, "limit": 1, "sort": "FromMax"}, false)
	if err != nil {
		return
	}
	posts := asObjects(data["posts"])
	if len(posts) != 1 || posts[0].str("id") != postID {
		return
	}
	post := posts[0]
	if caption, ok := post["caption"].(string); ok && caption != "" && len(caption) <= maxRubinoText {
		ref["caption"] = caption
	}
	if username, ok := post["post_profile_username"].(string); ok && len(username) <= 64 {
		ref["profile_username"] = username
	}
	payload["rubino_post"] = ref
	event.Payload, _ = json.Marshal(payload)
	for _, key := range []string{"full_file_url", "full_snapshot_url", "full_thumbnail_url"} {
		link := post.str(key)
		if link == "" || !c.validStoryURL(link) {
			continue
		}
		if size, contentType, ok := c.probeStoryImage(ctx, link); ok {
			c.setRubinoImage(event, postID, "rubino-post", link, size, contentType)
			return
		}
	}
}

// storyImageURL picks an image: the file itself for picture stories, otherwise
// the provider's snapshot (a still frame) or thumbnail.
func (c *Client) storyImageURL(story object) string {
	candidates := []string{"full_snapshot_url", "full_thumbnail_url"}
	if strings.EqualFold(story.str("story_type"), "Picture") {
		candidates = append([]string{"full_file_url"}, candidates...)
	}
	for _, key := range candidates {
		if link := story.str(key); link != "" && c.validStoryURL(link) {
			return link
		}
	}
	return ""
}

func extensionFor(contentType string) string {
	switch contentType {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ".jpg"
}

func (c *Client) cachedRubinoProfile(ctx context.Context) (string, error) {
	c.mu.RLock()
	profile := c.rubinoProfile
	c.mu.RUnlock()
	if profile != "" {
		return profile, nil
	}
	profile, err := c.defaultRubinoProfile(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.rubinoProfile = profile
	c.mu.Unlock()
	return profile, nil
}

// probeStoryImage reads the bounded size and image type without following
// redirects to other hosts.
func (c *Client) probeStoryImage(ctx context.Context, link string) (int64, string, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, link, nil)
	if err != nil {
		return 0, "", false
	}
	response, err := c.storyHTTP().Do(request)
	if err != nil {
		return 0, "", false
	}
	response.Body.Close()
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentType == "image/jpg" || contentType == "image/pjpeg" {
		contentType = "image/jpeg"
	}
	if response.StatusCode != http.StatusOK || response.ContentLength < 1 || response.ContentLength > min(c.cfg.MaxMediaBytes, 8<<20) || !strings.HasPrefix(contentType, "image/") {
		return 0, "", false
	}
	return response.ContentLength, contentType, true
}

func (c *Client) storyHTTP() *http.Client {
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

// downloadStoryMedia streams a registered story image reference.
func (c *Client) downloadStoryMedia(ctx context.Context, ref domains.ProviderMedia) (io.ReadCloser, error) {
	var private privateStoryMedia
	if json.Unmarshal(ref.Data, &private) != nil {
		return nil, protocolError()
	}
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	if private.Version != storyMediaVersion || private.Account != account || private.Size != ref.Size || private.Size < 1 || private.Size > c.cfg.MaxMediaBytes || !c.validStoryURL(private.URL) {
		return nil, domains.E("INVALID_MEDIA_REFERENCE", "media reference does not belong to this account", 400)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, private.URL, nil)
	if err != nil {
		return nil, protocolError()
	}
	response, err := c.storyHTTP().Do(request)
	if err != nil {
		return nil, domains.E("PROVIDER_TRANSPORT_ERROR", "Rubino media download failed", 502)
	}
	if response.StatusCode != http.StatusOK || (response.ContentLength >= 0 && response.ContentLength != private.Size) {
		response.Body.Close()
		return nil, domains.E("MEDIA_UNAVAILABLE", "Rubino story media is no longer available", 404)
	}
	return &exactReader{body: response.Body, remaining: private.Size}, nil
}

// exactReader fails instead of returning a truncated or oversized file.
type exactReader struct {
	body      io.ReadCloser
	remaining int64
}

func (r *exactReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		var one [1]byte
		if n, _ := r.body.Read(one[:]); n > 0 {
			return 0, protocolError()
		}
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.body.Read(p)
	r.remaining -= int64(n)
	if err == io.EOF && r.remaining > 0 {
		return n, io.ErrUnexpectedEOF
	}
	if err == io.EOF {
		err = nil
	}
	return n, err
}

func (r *exactReader) Close() error { return r.body.Close() }

// validStoryURL accepts only provider media hosts (or loopback in tests).
func (c *Client) validStoryURL(raw string) bool {
	return validEndpoint(raw, false, c.cfg.HTTPClient != nil)
}
