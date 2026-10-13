package rubikameow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func avatarPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func avatarReference() object {
	return object{"file_id": "123", "dc_id": "4", "access_hash_rec": "private-receive", "url": "https://untrusted.invalid/private"}
}
func stringInt(n int) string { b, _ := json.Marshal(n); return string(b) }
func TestAvatarUsesFreshAccountReferenceAndInspectsBytes(t *testing.T) {
	body := avatarPNG(t)
	lookups, downloads := 0, 0
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		lookups++
		if method != "getAvatars" || input.str("object_guid") != "u0peer" {
			t.Error("wrong lookup")
		}
		if lookups > 1 {
			return object{"avatars": []any{}}
		}
		return object{"avatars": []any{object{"main": avatarReference(), "thumbnail": avatarReference()}}}
	})
	original := c.http.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	c.http.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "messenger4.iranlms.ir" {
			return original.RoundTrip(r)
		}
		downloads++
		if r.URL.Scheme != "https" || r.URL.Path != "/GetFile.ashx" || r.Header.Get("access-hash-rec") != "private-receive" || r.Header.Get("auth") != fixtureAuth {
			t.Error("wrong media scope")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	stream, info, err := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "u0peer"}, "large")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(stream)
	_ = stream.Close()
	if !bytes.Equal(got, body) || info.ContentType != "image/png" || info.Width != 2 || info.Height != 3 {
		t.Fatalf("invalid projection: %#v", info)
	}
	_, _, err = c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "u0peer"}, "small")
	var de *domains.Error
	if !errors.As(err, &de) || de.Code != "AVATAR_NOT_FOUND" || downloads != 1 || lookups != 2 {
		t.Fatalf("stale reference reused: %v", err)
	}
}
func TestAvatarRejectsMalformedReferencesAndBytes(t *testing.T) {
	for _, kind := range []string{"dc", "large", "corrupt", "zero_size", "invalid_size"} {
		t.Run(kind, func(t *testing.T) {
			body := []byte("invalid-image")
			ref := avatarReference()
			if kind == "dc" {
				ref["dc_id"] = "4.evil"
			}
			if kind == "large" {
				ref["size"] = json.Number("8388609")
			}
			if kind == "zero_size" {
				ref["size"] = json.Number("0")
			}
			if kind == "invalid_size" {
				ref["size"] = "invalid"
			}
			c, _ := newRPCFixture(t, func(string, object, bool) object {
				return object{"avatars": []any{object{"main": ref, "thumbnail": ref}}}
			})
			original := c.http.Transport
			if original == nil {
				original = http.DefaultTransport
			}
			downloads := 0
			c.http.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "messenger4.iranlms.ir" {
					downloads++
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
				}
				return original.RoundTrip(r)
			})
			stream, _, err := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "u0peer"}, "large")
			if stream != nil || err == nil || (kind != "corrupt" && downloads != 0) {
				t.Fatalf("unsafe avatar accepted: %v", err)
			}
		})
	}
}

func TestAvatarAcceptsDeclaredSizeWhenPresent(t *testing.T) {
	body := avatarPNG(t)
	ref := avatarReference()
	ref["size"] = json.Number(stringInt(len(body)))
	c, _ := newRPCFixture(t, func(string, object, bool) object {
		return object{"avatars": []any{object{"main": ref, "thumbnail": ref}}}
	})
	original := c.http.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	c.http.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "messenger4.iranlms.ir" {
			return original.RoundTrip(r)
		}
		if r.Header.Get("start-index") != "0" || r.Header.Get("last-index") != stringInt(len(body)-1) {
			t.Error("declared-size avatar range changed")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	stream, info, err := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "u0peer"}, "small")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	got, err := io.ReadAll(stream)
	if err != nil || !bytes.Equal(got, body) || info.Size != int64(len(body)) {
		t.Fatalf("declared-size bytes changed: %v %#v", err, info)
	}
}

func TestAvatarWithoutSizeBoundsActualAndAdvertisedBodies(t *testing.T) {
	for _, kind := range []string{"chunked", "advertised"} {
		t.Run(kind, func(t *testing.T) {
			advertised := kind == "advertised"
			body := bytes.Repeat([]byte("x"), 1024)
			reader := bytes.NewReader(body)
			c, _ := newRPCFixture(t, func(string, object, bool) object {
				return object{"avatars": []any{object{"thumbnail": avatarReference()}}}
			})
			c.cfg.MaxMediaBytes = 128
			original := c.http.Transport
			if original == nil {
				original = http.DefaultTransport
			}
			c.http.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "messenger4.iranlms.ir" {
					return original.RoundTrip(r)
				}
				if r.Header.Get("start-index") != "0" || r.Header.Get("last-index") != "128" {
					t.Error("missing-size range was not bounded")
				}
				length := int64(-1)
				if advertised {
					length = int64(len(body))
				}
				return &http.Response{StatusCode: 200, ContentLength: length, Header: make(http.Header), Body: io.NopCloser(reader)}, nil
			})
			stream, _, err := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "u0peer"}, "small")
			var de *domains.Error
			if stream != nil || !errors.As(err, &de) || de.Code != "AVATAR_TOO_LARGE" {
				t.Fatalf("oversized unknown-length avatar accepted: %v", err)
			}
			consumed := len(body) - reader.Len()
			if (!advertised && consumed != 129) || (advertised && consumed != 0) {
				t.Fatalf("unbounded read: %d", consumed)
			}
		})
	}
}

func TestAvatarWithoutSizePreservesPrivateMissingResponse(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(stringInt(status), func(t *testing.T) {
			c, _ := newRPCFixture(t, func(string, object, bool) object {
				return object{"avatars": []any{object{"thumbnail": avatarReference()}}}
			})
			original := c.http.Transport
			if original == nil {
				original = http.DefaultTransport
			}
			c.http.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "messenger4.iranlms.ir" {
					return original.RoundTrip(r)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil))}, nil
			})
			stream, _, err := c.DownloadAvatar(context.Background(), domains.Peer{Type: "user", ID: "u0peer"}, "small")
			var de *domains.Error
			if stream != nil || !errors.As(err, &de) || de.Code != "AVATAR_NOT_FOUND" {
				t.Fatalf("private missing response exposed: %v", err)
			}
		})
	}
}
