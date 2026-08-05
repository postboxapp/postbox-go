package postbox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func newTestClient(t *testing.T, baseURL string, opts func(*Options)) *Client {
	t.Helper()
	o := Options{APIKey: "pb_live_test", BaseURL: baseURL}
	if opts != nil {
		opts(&o)
	}
	cl, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cl
}

func TestBearerAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"mbx_1","email":"s@acme.com"}}`))
	}))
	defer srv.Close()

	cl := newTestClient(t, srv.URL, nil)
	if _, err := cl.Mailboxes.Get(context.Background(), "mbx_1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotAuth != "Bearer pb_live_test" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer pb_live_test")
	}
}

func TestProjectHeaderOnlyWhenConfigured(t *testing.T) {
	var gotProject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProject = r.Header.Get("X-Project-Id")
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"mbx_1","email":"s@acme.com"}}`))
	}))
	defer srv.Close()

	// Without ProjectID: header absent.
	cl := newTestClient(t, srv.URL, nil)
	if _, err := cl.Mailboxes.Get(context.Background(), "mbx_1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotProject != "" {
		t.Fatalf("X-Project-Id = %q, want empty", gotProject)
	}

	// With ProjectID: header present.
	cl2 := newTestClient(t, srv.URL, func(o *Options) { o.ProjectID = "prj_42" })
	if _, err := cl2.Mailboxes.Get(context.Background(), "mbx_1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotProject != "prj_42" {
		t.Fatalf("X-Project-Id = %q, want %q", gotProject, "prj_42")
	}
}

func TestEnvelopeDataUnwrap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"mbx_1","email":"s@acme.com"}}`))
	}))
	defer srv.Close()

	cl := newTestClient(t, srv.URL, nil)
	mbx, err := cl.Mailboxes.Get(context.Background(), "mbx_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if mbx == nil {
		t.Fatal("mailbox is nil")
	}
	if mbx.Id != "mbx_1" || mbx.Email != "s@acme.com" {
		t.Fatalf("mailbox = %+v, want id=mbx_1 email=s@acme.com", mbx)
	}
}

func TestPostCarriesIdempotencyKey(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"key_1","apiKey":"pb_live_x","keyPrefix":"pb_live","label":"ci","permissions":[],"expiresAt":""}}`))
	}))
	defer srv.Close()

	cl := newTestClient(t, srv.URL, nil)
	_, err := cl.ApiKeys.Create(context.Background(), &ApiKeysCreateBody{Label: "ci"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotKey == "" {
		t.Fatal("Idempotency-Key header missing on POST")
	}
}

func TestValidationErrorStatus400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_abc")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"error":"bad input","code":"invalid"}`))
	}))
	defer srv.Close()

	cl := newTestClient(t, srv.URL, nil)
	_, err := cl.Mailboxes.Get(context.Background(), "mbx_1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	e, ok := asError(err)
	if !ok {
		t.Fatalf("error is not *Error: %T", err)
	}
	if e.Status != 400 {
		t.Fatalf("Status = %d, want 400", e.Status)
	}
	if e.RequestID != "req_abc" {
		t.Fatalf("RequestID = %q, want req_abc", e.RequestID)
	}
	if !IsValidation(err) {
		t.Fatal("IsValidation = false, want true")
	}
}

func TestRateLimitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"success":false,"error":"slow down","code":"rate_limited"}`))
	}))
	defer srv.Close()

	// Zero extra attempts so the 429 is returned immediately.
	cl := newTestClient(t, srv.URL, func(o *Options) { o.MaxRetries = -1 })
	_, err := cl.Mailboxes.Get(context.Background(), "mbx_1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !IsRateLimit(err) {
		t.Fatalf("IsRateLimit = false, want true (err=%v)", err)
	}
	e, _ := asError(err)
	if e.RetryAfter != 1 {
		t.Fatalf("RetryAfter = %d, want 1", e.RetryAfter)
	}
}

func TestRetryFiveHundredThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"success":false,"error":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"mbx_1","email":"s@acme.com"}}`))
	}))
	defer srv.Close()

	cl := newTestClient(t, srv.URL, nil) // MaxRetries defaults to 2
	mbx, err := cl.Mailboxes.Get(context.Background(), "mbx_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if mbx.Id != "mbx_1" {
		t.Fatalf("mailbox id = %q, want mbx_1", mbx.Id)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("server calls = %d, want 2 (one retry)", got)
	}
}

func TestNewRequiresAPIKey(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("expected error for empty APIKey")
	}
}

func TestDefaultsApplied(t *testing.T) {
	c, err := newClient(Options{APIKey: "k"})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	if c.baseURL != defaultBaseURL {
		t.Fatalf("baseURL = %q, want %q", c.baseURL, defaultBaseURL)
	}
	if c.timeoutMs != defaultTimeout {
		t.Fatalf("timeoutMs = %d, want %d", c.timeoutMs, defaultTimeout)
	}
	if c.maxRetries != defaultRetries {
		t.Fatalf("maxRetries = %d, want %d", c.maxRetries, defaultRetries)
	}
}

func TestBaseURLTrailingSlashTrimmed(t *testing.T) {
	c, _ := newClient(Options{APIKey: "k", BaseURL: "https://example.com/v1/"})
	if c.baseURL != "https://example.com/v1" {
		t.Fatalf("baseURL = %q, want trimmed", c.baseURL)
	}
}
