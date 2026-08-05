package postbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	sdkVersion      = "0.1.0"
	userAgent       = "postbox-go/" + sdkVersion
	defaultBaseURL  = "https://api.postboxapp.cloud/v1"
	defaultTimeout  = 30000
	defaultRetries  = 2
	backoffBaseMs   = 500.0
	backoffCapMs    = 8000.0
	acceptJSONValue = "application/json"
)

// client is the low-level transport shared by every generated resource. It
// applies auth, idempotency-key reuse across retries, exponential full-jitter
// backoff honouring Retry-After, a per-attempt timeout, and the {success,data}
// envelope unwrap.
type client struct {
	apiKey         string
	baseURL        string
	projectID      string
	timeoutMs      int
	maxRetries     int
	defaultHeaders map[string]string
	httpClient     *http.Client
}

// newClient validates options and applies defaults.
func newClient(opts Options) (*client, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, errors.New("postbox: APIKey is required")
	}

	baseURL := opts.BaseURL
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	timeoutMs := opts.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = defaultTimeout
	}
	if timeoutMs < 0 {
		timeoutMs = 0
	}

	maxRetries := opts.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultRetries
	}
	if maxRetries < 0 {
		maxRetries = 0
	}

	headers := make(map[string]string, len(opts.DefaultHeaders))
	for k, v := range opts.DefaultHeaders {
		headers[k] = v
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		// No client-level Timeout: the per-attempt deadline is enforced via
		// context so streaming requests are not killed.
		httpClient = &http.Client{}
	}

	return &client{
		apiKey:         opts.APIKey,
		baseURL:        baseURL,
		projectID:      opts.ProjectID,
		timeoutMs:      timeoutMs,
		maxRetries:     maxRetries,
		defaultHeaders: headers,
		httpClient:     httpClient,
	}, nil
}

// do is the single request primitive every generated method calls. T is the
// decoded shape of the response's data field.
func do[T any](c *client, ctx context.Context, method string, path string, query any, body any, idempotencyKey string) (T, error) {
	var zero T

	u, err := buildURL(c.baseURL, path, query)
	if err != nil {
		return zero, err
	}

	var rawBody []byte
	hasBody := body != nil
	if hasBody {
		rawBody, err = json.Marshal(body)
		if err != nil {
			return zero, err
		}
	}

	// The same idempotency key is reused across every retry of one logical call.
	idem := ""
	if method == http.MethodPost {
		idem = idempotencyKey
		if idem == "" {
			idem = newIdempotencyKey()
		}
	}

	maxAttempts := c.maxRetries + 1
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		respBody, status, headers, aerr := c.attempt(ctx, method, u, rawBody, hasBody, idem)
		if aerr != nil {
			// Network / timeout error. Do not retry if the caller cancelled.
			if attempt < maxAttempts && ctx.Err() == nil {
				lastErr = aerr
				if berr := c.backoff(ctx, attempt, ""); berr != nil {
					return zero, berr
				}
				continue
			}
			return zero, aerr
		}

		requestID := headers.Get("X-Request-Id")
		if status >= 200 && status < 300 {
			return unmarshalData[T](respBody)
		}

		apiErr := errorFromResponse(status, respBody, requestID, headers.Get("Retry-After"))
		if isRetryableStatus(status) && attempt < maxAttempts {
			lastErr = apiErr
			if berr := c.backoff(ctx, attempt, headers.Get("Retry-After")); berr != nil {
				return zero, berr
			}
			continue
		}
		return zero, apiErr
	}

	if lastErr != nil {
		return zero, lastErr
	}
	return zero, errors.New("postbox: request failed")
}

// attempt performs one HTTP round-trip with a per-attempt timeout derived from
// the caller's context.
func (c *client) attempt(ctx context.Context, method, u string, rawBody []byte, hasBody bool, idem string) ([]byte, int, http.Header, error) {
	reqCtx := ctx
	if c.timeoutMs > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, time.Duration(c.timeoutMs)*time.Millisecond)
		defer cancel()
	}

	var reader io.Reader
	if hasBody {
		reader = bytes.NewReader(rawBody)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, u, reader)
	if err != nil {
		return nil, 0, nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", acceptJSONValue)
	req.Header.Set("User-Agent", userAgent)
	for k, v := range c.defaultHeaders {
		req.Header.Set(k, v)
	}
	if c.projectID != "" {
		req.Header.Set("X-Project-Id", c.projectID)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	if hasBody {
		req.Header.Set("Content-Type", acceptJSONValue)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, nil, err
	}
	return b, resp.StatusCode, resp.Header, nil
}

// backoff sleeps before the next attempt using full-jitter exponential backoff,
// honouring a Retry-After header (seconds) when present. It returns early if the
// context is cancelled.
func (c *client) backoff(ctx context.Context, attempt int, retryAfter string) error {
	var delay time.Duration
	if retryAfter != "" {
		if sec, err := strconv.ParseFloat(strings.TrimSpace(retryAfter), 64); err == nil && sec >= 0 {
			delay = time.Duration(sec * float64(time.Second))
		}
	}
	if delay == 0 {
		expo := backoffBaseMs * math.Pow(2, float64(attempt-1))
		if expo > backoffCapMs {
			expo = backoffCapMs
		}
		delay = time.Duration(randFloat()*expo) * time.Millisecond
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isRetryableStatus reports whether an HTTP status is safe to retry: 429 and
// 5xx, but never 501.
func isRetryableStatus(status int) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if status == http.StatusNotImplemented {
		return false
	}
	return status >= 500
}

// unmarshalData decodes the envelope: it unwraps the "data" field when the root
// is an object carrying one, else decodes the whole body into T.
func unmarshalData[T any](body []byte) (T, error) {
	var zero T
	target := body

	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err == nil {
		if raw, ok := root["data"]; ok {
			target = raw
		}
	}

	trimmed := bytes.TrimSpace(target)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return zero, nil
	}

	var out T
	if err := json.Unmarshal(target, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// buildURL joins base + path and appends query params. query may be nil, an
// untyped nil, or a (possibly nil) pointer to a struct; nil fields are skipped.
func buildURL(baseURL, path string, query any) (string, error) {
	u := baseURL
	if strings.HasPrefix(path, "/") {
		u += path
	} else {
		u += "/" + path
	}
	if query == nil {
		return u, nil
	}

	b, err := json.Marshal(query)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		// Not a JSON object (e.g. a typed nil pointer marshals to "null").
		return u, nil
	}
	if len(m) == 0 {
		return u, nil
	}

	values := url.Values{}
	for k, v := range m {
		if v == nil {
			continue
		}
		switch vv := v.(type) {
		case []any:
			for _, item := range vv {
				if item == nil {
					continue
				}
				values.Add(k, stringifyScalar(item))
			}
		default:
			values.Add(k, stringifyScalar(vv))
		}
	}
	if len(values) == 0 {
		return u, nil
	}

	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + values.Encode(), nil
}

func stringifyScalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if !math.IsInf(t, 0) && t == math.Trunc(t) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// newIdempotencyKey returns a random UUID-v4-shaped string.
func newIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("idem-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// randFloat returns a crypto-seeded float in [0, 1) for jitter.
func randFloat() float64 {
	const prec = 1 << 53
	n, err := rand.Int(rand.Reader, big.NewInt(prec))
	if err != nil {
		return 0.5
	}
	return float64(n.Int64()) / float64(prec)
}
