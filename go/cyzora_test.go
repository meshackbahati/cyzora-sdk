package cyzora

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"0712345678":   "254712345678",
		"+254712345678": "254712345678",
		"254712345678": "254712345678",
		"712345678":    "254712345678",
		"12345":        "",
		"":             "",
	}
	for in, want := range cases {
		if got := NormalizePhone(in); got != want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCents(t *testing.T) {
	c, err := ToCents(2500)
	if err != nil || c != 250000 {
		t.Fatalf("ToCents(2500) = %d, %v", c, err)
	}
	if _, err := ToCents(0); err == nil {
		t.Fatal("ToCents(0) should error")
	}
	if FromCents(250000) != 2500 {
		t.Fatal("FromCents(250000) != 2500")
	}
}

func TestWebhookMultiSecret(t *testing.T) {
	c := New("sk_live_test", WithWebhookSecret("whsec_a"), WithWebhookSecret("whsec_b"))
	body := `{"event":"charge.succeeded","data":{"id":"ch_1"}}`

	if !c.VerifyWebhookSignature([]byte(body), sign("whsec_b", body)) {
		t.Error("expected whsec_b to verify")
	}
	if !c.VerifyWebhookSignature([]byte(body), sign("whsec_a", body)) {
		t.Error("expected whsec_a to verify")
	}
	if c.VerifyWebhookSignature([]byte(body), sign("whsec_wrong", body)) {
		t.Error("wrong secret should not verify")
	}
	if c.VerifyWebhookSignature([]byte(body), "") {
		t.Error("empty signature should not verify")
	}
}

func TestConstructWebhookEvent(t *testing.T) {
	c := New("sk_live_test", WithWebhookSecret("whsec_a"))
	payload := map[string]interface{}{"event": "payout.paid", "data": map[string]interface{}{"amount": 150000}}
	raw, _ := json.Marshal(payload)

	ev, err := c.ConstructWebhookEvent(raw, sign("whsec_a", string(raw)))
	if err != nil {
		t.Fatalf("ConstructWebhookEvent error: %v", err)
	}
	if ev["event"] != "payout.paid" {
		t.Errorf("event = %v, want payout.paid", ev["event"])
	}

	if _, err := c.ConstructWebhookEvent(raw, sign("bad", string(raw))); err == nil {
		t.Error("bad signature should error")
	}
	if _, err := c.ConstructWebhookEvent([]byte("not json"), sign("whsec_a", "not json")); err == nil {
		t.Error("bad json should error")
	}
}
