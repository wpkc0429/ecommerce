package media

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FakeStore is an in-memory ObjectStore for tests (design D2). PresignPut
// returns a URL on a private httptest server that enforces the same contract
// the real signature does: the PUT must carry exactly the signed Content-Type
// and Content-Length. It is production code only in the sense that tests in
// other packages import it; nothing in the server wires it.
type FakeStore struct {
	mu      sync.Mutex
	objects map[string]fakeObject
	pending map[string]fakePresign // token -> expectation
	reads   map[string]fakeRead    // token -> signed GET
	srv     *httptest.Server
	seq     int

	// FailPut / FailDelete make the corresponding calls return an error
	// (fault injection).
	FailPut, FailDelete bool
}

type fakeObject struct {
	body        []byte
	contentType string
	cache       string
}

type fakePresign struct {
	key, contentType string
	size             int64
}

// NewFakeStore starts the presign endpoint; Close stops it.
func NewFakeStore() *FakeStore {
	f := &FakeStore{objects: map[string]fakeObject{}, pending: map[string]fakePresign{}, reads: map[string]fakeRead{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.servePut))
	return f
}

func (f *FakeStore) Close() { f.srv.Close() }

func (f *FakeStore) servePut(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		f.serveGet(w, r)
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	tok := strings.TrimPrefix(r.URL.Path, "/")
	f.mu.Lock()
	exp, ok := f.pending[tok]
	f.mu.Unlock()
	if !ok {
		http.Error(w, "unknown or expired signature", http.StatusForbidden)
		return
	}
	if r.Header.Get("Content-Type") != exp.contentType || r.ContentLength != exp.size {
		http.Error(w, "SignatureDoesNotMatch", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || int64(len(body)) != exp.size {
		http.Error(w, "short body", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.objects[exp.key] = fakeObject{body: body, contentType: exp.contentType}
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

type fakeRead struct {
	key     string
	expires time.Time
}

func (f *FakeStore) serveGet(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.URL.Path, "/")
	f.mu.Lock()
	rd, ok := f.reads[tok]
	o, have := f.objects[rd.key]
	f.mu.Unlock()
	if !ok || !have || time.Now().After(rd.expires) {
		http.Error(w, "unknown or expired signature", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", o.contentType)
	_, _ = w.Write(o.body)
}

// PresignGet returns a URL on the private test server, valid for ttl.
func (f *FakeStore) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	tok := "get" + strconv.Itoa(f.seq)
	f.reads[tok] = fakeRead{key: key, expires: time.Now().Add(ttl)}
	return f.srv.URL + "/" + tok, nil
}

func (f *FakeStore) PresignPut(_ context.Context, key, contentType string, size int64, _ time.Duration) (PresignedPut, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	tok := "sig" + strconv.Itoa(f.seq)
	f.pending[tok] = fakePresign{key: key, contentType: contentType, size: size}
	return PresignedPut{URL: f.srv.URL + "/" + tok, Headers: map[string]string{"Content-Type": contentType}}, nil
}

func (f *FakeStore) Get(_ context.Context, key string, maxBytes int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[key]
	if !ok {
		return nil, ErrObjectNotFound
	}
	if int64(len(o.body)) > maxBytes {
		return nil, ErrObjectTooLarge
	}
	return append([]byte(nil), o.body...), nil
}

func (f *FakeStore) Put(_ context.Context, key string, body []byte, contentType, cacheControl string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailPut {
		return io.ErrUnexpectedEOF
	}
	f.objects[key] = fakeObject{body: append([]byte(nil), body...), contentType: contentType, cache: cacheControl}
	return nil
}

func (f *FakeStore) Delete(_ context.Context, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailDelete {
		return io.ErrUnexpectedEOF
	}
	for _, k := range keys {
		delete(f.objects, k)
	}
	return nil
}

func (f *FakeStore) List(_ context.Context, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Has reports whether key exists.
func (f *FakeStore) Has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

// Object returns a stored object's body, content type and Cache-Control.
func (f *FakeStore) Object(key string) (body []byte, contentType, cacheControl string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[key]
	return append([]byte(nil), o.body...), o.contentType, o.cache, ok
}

// Keys returns all keys under prefix (sorted).
func (f *FakeStore) Keys(prefix string) []string {
	k, _ := f.List(context.Background(), prefix)
	return k
}

// Seed stores an object directly (as if a browser had PUT it).
func (f *FakeStore) Seed(key string, body []byte, contentType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = fakeObject{body: append([]byte(nil), body...), contentType: contentType}
}
