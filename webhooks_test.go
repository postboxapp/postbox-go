package postbox

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookValid(t *testing.T) {
	payload := `{"event":"message.received","id":"msg_1"}`
	secret := "whsec_test"
	sig := sign(payload, secret)

	if !VerifyWebhook(payload, sig, secret) {
		t.Fatal("VerifyWebhook rejected a valid bare-hex signature")
	}
	if !VerifyWebhook(payload, "sha256="+sig, secret) {
		t.Fatal("VerifyWebhook rejected a valid sha256=-prefixed signature")
	}
	if !VerifyWebhook(payload, "t=123456,v1="+sig, secret) {
		t.Fatal("VerifyWebhook rejected a valid t=,v1= signature")
	}
}

func TestVerifyWebhookWrongSecret(t *testing.T) {
	payload := `{"event":"message.received"}`
	sig := sign(payload, "whsec_test")

	if VerifyWebhook(payload, sig, "whsec_wrong") {
		t.Fatal("VerifyWebhook accepted a signature under the wrong secret")
	}
}

func TestVerifyWebhookEmptyInputs(t *testing.T) {
	if VerifyWebhook("body", "", "secret") {
		t.Fatal("VerifyWebhook accepted an empty signature header")
	}
	if VerifyWebhook("body", "abc", "") {
		t.Fatal("VerifyWebhook accepted an empty secret")
	}
}
