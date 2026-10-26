package netease

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const sessionFileVersion = 1

type storedSession struct {
	Version int            `json:"version"`
	Cookies []storedCookie `json:"cookies"`
}

type storedCookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Domain   string    `json:"domain"`
	Path     string    `json:"path"`
	Expires  time.Time `json:"expires,omitempty"`
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"http_only,omitempty"`
	HostOnly bool      `json:"host_only"`
}

type sessionJar struct {
	mu       sync.RWMutex
	path     string
	cookies  map[string]storedCookie
	writeErr error
}

func loadSessionJar(path string) (*sessionJar, error) {
	if path == "" {
		return nil, fmt.Errorf("credential path is empty")
	}
	jar := &sessionJar{path: path, cookies: make(map[string]storedCookie)}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return jar, nil
		}
		return nil, fmt.Errorf("open credentials")
	}
	file := os.NewFile(uintptr(fd), "netease credentials")
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("credentials must be a regular file")
	}
	if err := file.Chmod(0600); err != nil {
		return nil, fmt.Errorf("secure credentials")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumBodyBytes+1))
	if err != nil || len(data) > maximumBodyBytes {
		return nil, fmt.Errorf("read credentials")
	}

	var stored storedSession
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("malformed credentials")
	}
	if stored.Version != sessionFileVersion {
		return nil, fmt.Errorf("unsupported credentials format")
	}
	for _, cookie := range stored.Cookies {
		if !validStoredCookie(cookie) || cookieExpired(cookie, time.Now()) {
			continue
		}
		jar.cookies[cookieKey(cookie)] = cookie
	}
	return jar, nil
}

func (j *sessionJar) Cookies(target *url.URL) []*http.Cookie {
	if target == nil {
		return nil
	}
	now := time.Now()
	host := canonicalDomain(target.Hostname())
	requestPath := target.EscapedPath()
	if requestPath == "" {
		requestPath = "/"
	}

	j.mu.RLock()
	cookies := make([]*http.Cookie, 0, len(j.cookies))
	for _, stored := range j.cookies {
		if cookieExpired(stored, now) || !cookieMatches(stored, host, requestPath, target.Scheme == "https") {
			continue
		}
		cookies = append(cookies, &http.Cookie{
			Name:     stored.Name,
			Value:    stored.Value,
			Path:     stored.Path,
			Secure:   stored.Secure,
			HttpOnly: stored.HTTPOnly,
		})
	}
	j.mu.RUnlock()

	sort.Slice(cookies, func(i, k int) bool {
		if len(cookies[i].Path) == len(cookies[k].Path) {
			return cookies[i].Name < cookies[k].Name
		}
		return len(cookies[i].Path) > len(cookies[k].Path)
	})
	return cookies
}

func (j *sessionJar) SetCookies(target *url.URL, incoming []*http.Cookie) {
	if target == nil || len(incoming) == 0 {
		return
	}
	host := canonicalDomain(target.Hostname())
	if host == "" {
		return
	}
	now := time.Now()

	j.mu.Lock()
	changed := false
	for _, cookie := range incoming {
		if cookie == nil || cookie.Name == "" {
			continue
		}
		stored, ok := canonicalStoredCookie(target, cookie)
		if !ok {
			continue
		}
		key := cookieKey(stored)
		if cookie.MaxAge < 0 || cookieExpired(stored, now) {
			if _, present := j.cookies[key]; present {
				delete(j.cookies, key)
				changed = true
			}
			continue
		}
		j.cookies[key] = stored
		changed = true
	}
	if changed {
		if err := j.persistLocked(); err != nil && j.writeErr == nil {
			j.writeErr = err
		}
	}
	j.mu.Unlock()
}

func (j *sessionJar) cookieValue(rawURL, name string) string {
	target, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, cookie := range j.Cookies(target) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func (j *sessionJar) takeWriteError() error {
	j.mu.Lock()
	err := j.writeErr
	j.writeErr = nil
	j.mu.Unlock()
	return err
}

func (j *sessionJar) persistLocked() error {
	cookies := make([]storedCookie, 0, len(j.cookies))
	now := time.Now()
	for key, cookie := range j.cookies {
		if cookieExpired(cookie, now) {
			delete(j.cookies, key)
			continue
		}
		cookies = append(cookies, cookie)
	}
	sort.Slice(cookies, func(i, k int) bool {
		return cookieKey(cookies[i]) < cookieKey(cookies[k])
	})
	payload, err := json.Marshal(storedSession{Version: sessionFileVersion, Cookies: cookies})
	if err != nil {
		return fmt.Errorf("encode credentials")
	}
	if err := writePrivateFile(j.path, payload); err != nil {
		return fmt.Errorf("write credentials")
	}
	return nil
}

// ImportCookies imports a Netscape-format cookie file into this adapter's
// private credentials. It never reads a cookie file unless explicitly called.
func (a *Adapter) ImportCookies(path string) error {
	if a == nil || a.jar == nil {
		return fmt.Errorf("netease import cookies: adapter is not initialized")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("netease import cookies: read file")
	}
	defer func() { _ = file.Close() }()

	var imported []storedCookie
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4<<10), maximumBodyBytes)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSuffix(scanner.Text(), "\r")
		if text == "" || (strings.HasPrefix(text, "#") && !strings.HasPrefix(text, "#HttpOnly_")) {
			continue
		}
		httpOnly := strings.HasPrefix(text, "#HttpOnly_")
		if httpOnly {
			text = strings.TrimPrefix(text, "#HttpOnly_")
		}
		fields := strings.Split(text, "\t")
		if len(fields) != 7 {
			return fmt.Errorf("netease import cookies: malformed Netscape record at line %d", line)
		}
		expires, err := parseNetscapeExpiry(fields[4])
		if err != nil {
			return fmt.Errorf("netease import cookies: malformed expiry at line %d", line)
		}
		cookie := storedCookie{
			Name:     fields[5],
			Value:    fields[6],
			Domain:   canonicalDomain(fields[0]),
			Path:     fields[2],
			Expires:  expires,
			Secure:   strings.EqualFold(fields[3], "true"),
			HTTPOnly: httpOnly,
			HostOnly: !strings.EqualFold(fields[1], "true"),
		}
		if !validStoredCookie(cookie) {
			return fmt.Errorf("netease import cookies: malformed record at line %d", line)
		}
		if !cookieExpired(cookie, time.Now()) {
			imported = append(imported, cookie)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("netease import cookies: read file")
	}

	a.jar.mu.Lock()
	for _, cookie := range imported {
		a.jar.cookies[cookieKey(cookie)] = cookie
	}
	if err := a.jar.persistLocked(); err != nil {
		a.jar.mu.Unlock()
		return fmt.Errorf("netease import cookies: persist credentials")
	}
	a.jar.mu.Unlock()
	return nil
}

func parseNetscapeExpiry(value string) (time.Time, error) {
	if value == "0" || value == "" {
		return time.Time{}, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func canonicalStoredCookie(target *url.URL, cookie *http.Cookie) (storedCookie, bool) {
	host := canonicalDomain(target.Hostname())
	domain := canonicalDomain(cookie.Domain)
	hostOnly := domain == ""
	if hostOnly {
		domain = host
	}
	if domain == "" || (!hostOnly && !domainMatches(host, domain)) {
		return storedCookie{}, false
	}
	path := cookie.Path
	if path == "" || path[0] != '/' {
		path = defaultCookiePath(target.EscapedPath())
	}
	expires := cookie.Expires
	if cookie.MaxAge > 0 {
		expires = time.Now().Add(time.Duration(cookie.MaxAge) * time.Second)
	}
	return storedCookie{
		Name:     cookie.Name,
		Value:    cookie.Value,
		Domain:   domain,
		Path:     path,
		Expires:  expires,
		Secure:   cookie.Secure,
		HTTPOnly: cookie.HttpOnly,
		HostOnly: hostOnly,
	}, true
}

func validStoredCookie(cookie storedCookie) bool {
	return cookie.Name != "" && cookie.Domain != "" && strings.HasPrefix(cookie.Path, "/")
}

func cookieKey(cookie storedCookie) string {
	return cookie.Domain + "\x00" + cookie.Path + "\x00" + cookie.Name
}

func cookieExpired(cookie storedCookie, now time.Time) bool {
	return !cookie.Expires.IsZero() && !cookie.Expires.After(now)
}

func cookieMatches(cookie storedCookie, host, requestPath string, secure bool) bool {
	if cookie.Secure && !secure {
		return false
	}
	if cookie.HostOnly {
		if host != cookie.Domain {
			return false
		}
	} else if !domainMatches(host, cookie.Domain) {
		return false
	}
	return pathMatches(requestPath, cookie.Path)
}

func canonicalDomain(domain string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

func domainMatches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func defaultCookiePath(requestPath string) string {
	if requestPath == "" || requestPath[0] != '/' {
		return "/"
	}
	index := strings.LastIndex(requestPath, "/")
	if index <= 0 {
		return "/"
	}
	return requestPath[:index]
}

func pathMatches(requestPath, cookiePath string) bool {
	if requestPath == cookiePath {
		return true
	}
	if !strings.HasPrefix(requestPath, cookiePath) {
		return false
	}
	return strings.HasSuffix(cookiePath, "/") || (len(requestPath) > len(cookiePath) && requestPath[len(cookiePath)] == '/')
}

func writePrivateFile(path string, content []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe credentials directory")
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("unsafe credential file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
