package netease

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LingLambda/arcana-toc/internal/platform"
)

type refreshTransport func(*http.Request) (*http.Response, error)

func (f refreshTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func decryptWeapiPayload(t *testing.T, req *http.Request) map[string]interface{} {
	t.Helper()
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	decode := func(encoded, key string) []byte {
		t.Helper()
		body, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		block, err := aes.NewCipher([]byte(key))
		if err != nil {
			t.Fatal(err)
		}
		if len(body) == 0 || len(body)%block.BlockSize() != 0 {
			t.Fatal("invalid weapi ciphertext")
		}
		cipher.NewCBCDecrypter(block, []byte("0102030405060708")).CryptBlocks(body, body)
		padding := int(body[len(body)-1])
		if padding < 1 || padding > block.BlockSize() || padding > len(body) {
			t.Fatal("invalid weapi padding")
		}
		return body[:len(body)-padding]
	}
	inner := decode(req.PostForm.Get("params"), "AAAAAAAAAAAAAAAA")
	var payload map[string]interface{}
	if err := json.Unmarshal(decode(string(inner), "0CoJUm6Qyw8W8jud"), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

// The encoder draws its temporary AES key through crypto/rand.Reader. Zero
// bytes select 'A' for all sixteen characters, so this test can inspect the
// encrypted wire payload without changing production encryption.
func TestRefreshCSRFAndCookieRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	a, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(apiBase)
	a.jar.SetCookies(target, []*http.Cookie{
		{Name: "MUSIC_U", Value: "old-session", Path: "/", Secure: true},
		{Name: "__csrf", Value: "old-csrf", Path: "/", Secure: true},
	})
	originalReader := rand.Reader
	rand.Reader = bytes.NewReader(make([]byte, 4096))
	defer func() { rand.Reader = originalReader }()

	calls := 0
	a.client.Transport = refreshTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.URL.String() != apiBase+"login/token/refresh" {
			t.Fatalf("unexpected refresh request: %s %s", req.Method, req.URL)
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if req.PostForm.Get("encSecKey") == "" {
			t.Fatal("missing encrypted weapi key")
		}
		payload := decryptWeapiPayload(t, req)
		wantSession, wantCSRF := "old-session", "old-csrf"
		if calls > 1 {
			wantSession, wantCSRF = "new-session", "new-csrf"
		}
		csrf, err := req.Cookie("__csrf")
		if err != nil || csrf.Value != wantCSRF || payload["csrf_token"] != wantCSRF {
			t.Fatalf("CSRF cookie/payload mismatch: cookie=%v payload=%v err=%v", csrf, payload, err)
		}
		session, err := req.Cookie("MUSIC_U")
		if err != nil || session.Value != wantSession {
			t.Fatalf("refresh session cookie = %v, error = %v", session, err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{"Set-Cookie": {
				"MUSIC_U=new-session; Path=/; Secure; HttpOnly",
				"__csrf=new-csrf; Path=/; Secure",
			}},
			Body: io.NopCloser(strings.NewReader(`{"code":200}`)),
		}, nil
	})
	if err := a.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	restored.client.Transport = a.client.Transport
	if err := restored.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshRequiresAuthenticatedSession(t *testing.T) {
	a, err := New(filepath.Join(t.TempDir(), "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	a.client.Transport = refreshTransport(func(req *http.Request) (*http.Response, error) {
		t.Fatal("anonymous refresh must not reach provider")
		return nil, nil
	})
	if err := a.Refresh(context.Background()); !errors.Is(err, platform.ErrNotLoggedIn) {
		t.Fatalf("anonymous refresh error = %v", err)
	}
}

func TestRefreshRejectsExpiredSession(t *testing.T) {
	a, err := New(filepath.Join(t.TempDir(), "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(apiBase)
	a.jar.SetCookies(target, []*http.Cookie{{Name: "MUSIC_U", Value: "expired-session", Path: "/", Secure: true}})
	a.client.Transport = refreshTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":301}`))}, nil
	})
	if err := a.Refresh(context.Background()); !errors.Is(err, platform.ErrNotLoggedIn) {
		t.Fatalf("expired refresh error = %v", err)
	}
	if got := a.jar.cookieValue(apiBase, "MUSIC_U"); got != "expired-session" {
		t.Fatalf("failed refresh replaced prior credentials: %q", got)
	}
}

func TestQRLoginOmitsSessionCSRF(t *testing.T) {
	a, err := New(filepath.Join(t.TempDir(), "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(apiBase)
	a.jar.SetCookies(target, []*http.Cookie{{Name: "__csrf", Value: "session-csrf", Path: "/", Secure: true}})
	originalReader := rand.Reader
	rand.Reader = bytes.NewReader(make([]byte, 4096))
	defer func() { rand.Reader = originalReader }()
	a.client.Transport = refreshTransport(func(req *http.Request) (*http.Response, error) {
		payload := decryptWeapiPayload(t, req)
		if _, present := payload["csrf_token"]; present {
			t.Fatal("QR request must not include session CSRF in its encrypted payload")
		}
		if _, err := req.Cookie("__csrf"); err != http.ErrNoCookie {
			t.Fatal("QR request must not include session CSRF in its Cookie header")
		}
		body := `{"code":801}`
		if strings.HasSuffix(req.URL.Path, "/unikey") {
			body = `{"code":200,"unikey":"qr-key"}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	qr, err := a.QRStart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	code, err := a.QRPoll(context.Background(), qr.Key)
	if err != nil || code != 801 {
		t.Fatalf("QR poll = %d, %v", code, err)
	}
	if a.jar.cookieValue(apiBase, "__csrf") != "session-csrf" {
		t.Fatal("QR request modified the authenticated session")
	}
}
