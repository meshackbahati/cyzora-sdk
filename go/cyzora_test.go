package cyzora

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"0712345678":    "254712345678",
		"+254712345678": "254712345678",
		"254712345678":  "254712345678",
		"712345678":     "254712345678",
		"12345":         "",
		"":              "",
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
	if _, err := c.ConstructWebhookEvent(raw, sign("whsec_a", string(raw)), "payout.failed"); err == nil {
		t.Error("mismatched event header should error")
	}
}

func TestEnvDefaults(t *testing.T) {
	t.Setenv("CYZORA_API_BASE", "https://example.test/api/v1/")
	t.Setenv("CYZORA_WEBHOOK_SECRET", "whsec_env_a, whsec_env_b")
	c := New("sk_live_x")
	if c.baseURL != "https://example.test/api/v1" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
	body := `{"event":"charge.succeeded"}`
	if !c.VerifyWebhookSignature([]byte(body), sign("whsec_env_b", body)) {
		t.Error("expected the comma-separated env secret to verify")
	}
}

func TestCreateChargePayload(t *testing.T) {
	var gotMethod, gotPath, gotKey, gotIdem, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotKey, gotIdem = r.Header.Get("X-API-Key"), r.Header.Get("Idempotency-Key")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Write([]byte(`{"id":"chg_1","status":"pending"}`))
	}))
	defer server.Close()

	c := New("sk_live_x", WithBaseURL(server.URL))
	_, err := c.CreateCharge(context.Background(), ChargeParams{
		AmountKes: 2500.4, Phone: "0712345678", Network: "tkash", IdempotencyKey: "INV-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/charges" {
		t.Errorf("got %s %s", gotMethod, gotPath)
	}
	if gotKey != "sk_live_x" || gotIdem != "INV-1" {
		t.Errorf("headers: key=%q idempotency=%q", gotKey, gotIdem)
	}
	if !strings.Contains(gotBody, `"amount":250100`) || !strings.Contains(gotBody, `"network":"tkash"`) {
		t.Errorf("body = %s", gotBody)
	}
}

func TestPayoutValidation(t *testing.T) {
	c := New("sk_live_x")
	if _, err := c.CreatePayout(context.Background(), PayoutParams{
		AmountKes: 1500.5, Phone: "0712345678",
	}); err == nil {
		t.Error("fractional payout should error")
	}
	if _, err := c.CreatePayout(context.Background(), PayoutParams{
		AmountKes: 1500, DestinationType: "paybill", ShortCode: "100000",
	}); err == nil {
		t.Error("paybill payout without account_ref should error")
	}
}

func TestFeePreviewQuery(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"fee":3300}`))
	}))
	defer server.Close()

	c := New("sk_live_x", WithBaseURL(server.URL))
	if _, err := c.PayoutFeePreview(context.Background(), 2500, "paybill", "collections"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"amount=250000", "destination=paybill", "wallet=collections"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if strings.Contains(gotQuery, "destination_type") {
		t.Errorf("query %q uses the wrong parameter name", gotQuery)
	}
}

func TestPublicStatementAndExport(t *testing.T) {
	var noKey bool
	var csv string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/public/") {
			noKey = r.Header.Get("X-API-Key") == ""
			w.Write([]byte(`{"valid":true}`))
			return
		}
		w.Write([]byte("date,amount\n2026-10-01,2500.00\n"))
	}))
	defer server.Close()

	c := New("sk_live_x", WithBaseURL(server.URL))
	result, err := c.VerifyStatement(context.Background(), "K7PX3MZQ9H2D")
	if err != nil || result["valid"] != true || !noKey {
		t.Fatalf("VerifyStatement: result=%v err=%v noKey=%v", result, err, noKey)
	}
	csv, err = c.ExportCharges(context.Background(), map[string]string{"status": "succeeded"})
	if err != nil || !strings.HasPrefix(csv, "date,amount") {
		t.Fatalf("ExportCharges: csv=%q err=%v", csv, err)
	}
}

// recorder answers every request with reply and remembers the last one.
type recorder struct {
	method, uri, idem string
	body              map[string]interface{}
}

func newRecorder(t *testing.T, reply string) (*recorder, *Client, func()) {
	t.Helper()
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method, rec.uri, rec.idem = r.Method, r.URL.RequestURI(), r.Header.Get("Idempotency-Key")
		rec.body = nil
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &rec.body)
		}
		w.Write([]byte(reply))
	}))
	return rec, New("sk_live_x", WithBaseURL(server.URL)), server.Close
}

func TestChargeMetadataAndDescription(t *testing.T) {
	rec, c, done := newRecorder(t, `{}`)
	defer done()
	_, err := c.CreateCharge(context.Background(), ChargeParams{
		AmountKes: 100, Phone: "0712345678", Description: "ignored",
		Metadata: map[string]interface{}{"order_id": 1001, "paid": false, "skip": nil, "cart": map[string]int{"a": 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.body["description"]; ok {
		t.Error("description should not be sent")
	}
	want := map[string]interface{}{"order_id": "1001", "paid": "false", "cart": `{"a":1}`}
	got, _ := rec.body["metadata"].(map[string]interface{})
	if len(got) != len(want) {
		t.Fatalf("metadata = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("metadata[%s] = %v, want %v", k, got[k], v)
		}
	}

	many := map[string]interface{}{}
	for i := 0; i < 21; i++ {
		many[fmt.Sprintf("k%d", i)] = i
	}
	if _, err := c.CreateCharge(context.Background(), ChargeParams{AmountKes: 100, Phone: "0712345678", Metadata: many}); err == nil {
		t.Error("21 metadata keys should be refused")
	}
}

func TestListsDecodeArrays(t *testing.T) {
	rec, c, done := newRecorder(t, `[{"id":"a"},{"id":"b"}]`)
	defer done()
	charges, err := c.ListCharges(context.Background(), ListChargesParams{Status: "succeeded", FromDate: "2026-10-01", Mode: "all"})
	if err != nil || len(charges) != 2 {
		t.Fatalf("charges=%v err=%v", charges, err)
	}
	if rec.uri != "/charges?from_date=2026-10-01&mode=all&status=succeeded" {
		t.Errorf("uri = %s", rec.uri)
	}
	payouts, err := c.ListPayouts(context.Background(), nil)
	if err != nil || len(payouts) != 2 || payouts[1]["id"] != "b" {
		t.Fatalf("payouts=%v err=%v", payouts, err)
	}
	if _, err := c.ListPayoutBatches(context.Background()); err != nil {
		t.Fatal(err)
	}
	var raw []map[string]interface{}
	if err := c.RequestInto(context.Background(), http.MethodGet, "/payouts", nil, "", nil, &raw); err != nil || len(raw) != 2 {
		t.Fatalf("RequestInto=%v err=%v", raw, err)
	}
}

func TestWalletMoves(t *testing.T) {
	rec, c, done := newRecorder(t, `{}`)
	defer done()
	ctx := context.Background()
	if _, err := c.PreviewWalletMove(ctx, 1500.5, 0); err != nil || rec.uri != "/wallets/move/preview?amount=150050" {
		t.Fatalf("uri=%s err=%v", rec.uri, err)
	}
	if _, err := c.MoveToPayouts(ctx, WalletMoveParams{AmountKes: 2000, IdempotencyKey: "MV-1"}); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPost || rec.uri != "/wallets/move" || rec.body["amount"] != float64(200000) || rec.idem != "MV-1" {
		t.Errorf("move: %s %s %v %q", rec.method, rec.uri, rec.body, rec.idem)
	}
	if _, err := c.CreateSubsidiaryTransfer(ctx, SubsidiaryTransferParams{SubsidiaryID: "sub_1", AmountKes: 500, FromWallet: "payouts", IdempotencyKey: "ST-1"}); err != nil {
		t.Fatal(err)
	}
	if rec.body["subsidiary_id"] != "sub_1" || rec.body["amount"] != float64(50000) || rec.body["from_wallet"] != "payouts" {
		t.Errorf("subsidiary body = %v", rec.body)
	}
	if _, err := c.ListWalletTransfers(ctx, 10); err != nil || rec.uri != "/wallets/transfers?limit=10" {
		t.Errorf("uri=%s err=%v", rec.uri, err)
	}
	if _, err := c.MoveToPayouts(ctx, WalletMoveParams{}); err == nil {
		t.Error("zero move should be refused")
	}
	if _, err := c.CreateSubsidiaryTransfer(ctx, SubsidiaryTransferParams{AmountKes: 500}); err == nil {
		t.Error("missing subsidiary should be refused")
	}
}
