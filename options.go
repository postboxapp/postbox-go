package postbox

import "net/http"

// Options configures a Postbox client. Only APIKey is required; every other
// field has a sensible default applied in newClient.
type Options struct {
	// APIKey is the bearer credential (pb_live_... or pb_test_...). Required.
	APIKey string
	// BaseURL is the API root. Defaults to https://api.postboxapp.cloud/v1.
	BaseURL string
	// ProjectID, when set, is sent as the X-Project-Id header on every request.
	ProjectID string
	// TimeoutMs is the per-attempt timeout in milliseconds. Defaults to 30000.
	TimeoutMs int
	// MaxRetries is the number of EXTRA attempts on retryable failures.
	// Defaults to 2.
	MaxRetries int
	// DefaultHeaders are merged into every request.
	DefaultHeaders map[string]string
	// HTTPClient lets callers inject a custom *http.Client (proxy, transport).
	// When nil a default client is used.
	HTTPClient *http.Client
}
