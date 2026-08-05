package postbox

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// VerifyWebhook reports whether signatureHeader is a valid HMAC-SHA256 signature
// of payload under secret. The comparison is timing-safe. The header may be a
// bare hex digest, a "sha256=<hex>" value, or a structured "t=<ts>,v1=<hex>"
// value; every candidate signature it contains is checked.
func VerifyWebhook(payload, signatureHeader, secret string) bool {
	if secret == "" || signatureHeader == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expected := []byte(hex.EncodeToString(mac.Sum(nil)))

	for _, cand := range extractSignatures(signatureHeader) {
		candidate := []byte(strings.ToLower(strings.TrimSpace(cand)))
		if hmac.Equal(candidate, expected) {
			return true
		}
	}
	return false
}

// extractSignatures pulls the candidate hex digests out of a signature header,
// tolerating bare hex, "sha256=" prefixes, and "t=...,v1=..." structures.
func extractSignatures(header string) []string {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}

	// Structured comma-separated pairs (t=...,v1=...).
	if strings.Contains(header, ",") || strings.Contains(header, "v1=") {
		var out []string
		for _, part := range strings.Split(header, ",") {
			part = strings.TrimSpace(part)
			if i := strings.Index(part, "="); i >= 0 {
				key := strings.TrimSpace(part[:i])
				val := strings.TrimSpace(part[i+1:])
				switch key {
				case "v1", "sha256", "signature", "s":
					out = append(out, val)
				}
			} else if part != "" {
				out = append(out, part)
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	if strings.HasPrefix(header, "sha256=") {
		return []string{strings.TrimPrefix(header, "sha256=")}
	}
	return []string{header}
}
