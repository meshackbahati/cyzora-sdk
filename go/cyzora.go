// Package cyzora provides a client for the Cyzora M-Pesa payment rail.
//
// Amounts on the wire are integer cents of whole shillings (KES 2,500 -> 250000).
// Every money-moving POST should carry an idempotency key so a retry cannot
// double-charge or double-pay.
//
//	client := cyzora.New("sk_live_...", cyzora.WithWebhookSecret("whsec_..."))
//	charge, err := client.CreateCharge(ctx, cyzora.ChargeParams{
//	    AmountKes: 100, Phone: "0712345678", AccountRef: "INV-1", IdempotencyKey: "INV-1",
//	})
package cyzora

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultBaseURL   = "https://api.cyzora.co.ke/api/v1"
	PayoutMinCents   = 3000 // KES 30
)

// Error is returned for any non-2xx response or transport failure.
type Error struct {
	Message string
	Status  int
	Body    map[string]interface{}
}

func (e *Error) Error() string { return e.Message }

type Client struct {
	secretKey      string
	baseURL        string
	webhookSecrets []string
	http           *http.Client
	maxRetries     int
}

// Known webhook event names. The rail evolves, so also compare raw event strings.
const (
	EventChargeSucceeded = "charge.succeeded"
	EventChargeFailed    = "charge.failed"
	EventChargeExpired   = "charge.expired"
	EventPayoutPaid      = "payout.paid"
	EventPayoutFailed    = "payout.failed"
)

type Option func(*Client)

func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

func WithWebhookSecret(s string) Option {
	return func(c *Client) { c.webhookSecrets = append(c.webhookSecrets, s) }
}

func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithMaxRetries sets automatic retries on network/5xx/429 errors (default 2).
// Retries only happen for GETs or when an idempotency key is set.
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = n }
}

// New builds a Cyzora client. secretKey is the sk_live_... key.
func New(secretKey string, opts ...Option) *Client {
	c := &Client{
		secretKey:  secretKey,
		baseURL:    DefaultBaseURL,
		http:       &http.Client{Timeout: 30 * time.Second},
		maxRetries: 2,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

var phoneRE = regexp.MustCompile(`^254[17]\d{8}$`)

// NormalizePhone converts a Kenyan number to the 254XXXXXXXXX form the rail expects.
func NormalizePhone(phone string) string {
	p := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '+':
			return -1
		}
		return r
	}, phone)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "0") {
		p = "254" + p[1:]
	} else if strings.HasPrefix(p, "7") || strings.HasPrefix(p, "1") {
		p = "254" + p
	}
	if phoneRE.MatchString(p) {
		return p
	}
	return ""
}

// ToCents converts shillings to integer cents, rounding to whole shillings first.
func ToCents(amountKes float64) (int64, error) {
	kes := int64(math.Round(amountKes))
	if kes < 1 {
		return 0, &Error{Message: "amount must be at least KES 1"}
	}
	return kes * 100, nil
}

// FromCents converts integer cents back to shillings.
func FromCents(cents int64) float64 { return float64(cents) / 100 }

func (c *Client) do(ctx context.Context, method, path string, body interface{}, idempotencyKey string, query map[string]string, out interface{}) error {
	u := c.baseURL + path
	if len(query) > 0 {
		q := url.Values{}
		for k, v := range query {
			if v != "" {
				q.Set(k, v)
			}
		}
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
	}

	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyBytes = b
	}
	if len(idempotencyKey) > 128 {
		idempotencyKey = idempotencyKey[:128]
	}
	// Retrying is only safe when the rail can dedupe: GETs, or POSTs with an idempotency key.
	canRetry := strings.ToUpper(method) == "GET" || idempotencyKey != ""

	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if bodyBytes != nil {
			reader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, reader)
		if err != nil {
			return err
		}
		req.Header.Set("X-API-Key", c.secretKey)
		req.Header.Set("Accept", "application/json")
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if canRetry && attempt < c.maxRetries {
				time.Sleep(time.Duration(250*(1<<attempt)) * time.Millisecond)
				continue
			}
			return &Error{Message: fmt.Sprintf("could not reach Cyzora: %v", err)}
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		data := map[string]interface{}{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &data)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			transient := resp.StatusCode >= 500 || resp.StatusCode == 429
			if transient && canRetry && attempt < c.maxRetries {
				time.Sleep(time.Duration(250*(1<<attempt)) * time.Millisecond)
				continue
			}
			msg := fmt.Sprintf("Cyzora request failed (%d)", resp.StatusCode)
			if m, ok := data["error"].(string); ok && m != "" {
				msg = m
			} else if m, ok := data["message"].(string); ok && m != "" {
				msg = m
			}
			return &Error{Message: msg, Status: resp.StatusCode, Body: data}
		}
		if out != nil && len(raw) > 0 {
			return json.Unmarshal(raw, out)
		}
		return nil
	}
}

// Request is an escape hatch for any endpoint, including new ones the rail adds
// before this SDK ships a typed wrapper. path is appended to the base URL.
func (c *Client) Request(ctx context.Context, method, path string, body interface{}, idempotencyKey string, query map[string]string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, method, path, body, idempotencyKey, query, &out)
	return out, err
}

// ChargeParams describes an STK push.
type ChargeParams struct {
	AmountKes      float64
	AmountCents    int64
	Phone          string
	Network        string
	AccountRef     string
	Description    string
	Metadata       map[string]interface{}
	IdempotencyKey string
}

// CreateCharge sends an M-Pesa STK prompt to a customer's phone.
func (c *Client) CreateCharge(ctx context.Context, p ChargeParams) (map[string]interface{}, error) {
	phone := NormalizePhone(p.Phone)
	if phone == "" {
		return nil, &Error{Message: "enter a valid Safaricom phone number (e.g. 0712 345 678)"}
	}
	cents := p.AmountCents
	if cents == 0 {
		var err error
		if cents, err = ToCents(p.AmountKes); err != nil {
			return nil, err
		}
	}
	if cents < 100 {
		return nil, &Error{Message: "amount must be a whole number of shillings, at least KES 1"}
	}
	body := map[string]interface{}{
		"amount":  cents,
		"phone":   phone,
		"network": orDefault(p.Network, "mpesa"),
	}
	if p.AccountRef != "" {
		body["account_ref"] = trunc(p.AccountRef, 64)
	}
	if p.Description != "" {
		body["description"] = trunc(p.Description, 200)
	}
	if p.Metadata != nil {
		body["metadata"] = p.Metadata
	}
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodPost, "/charges", body, p.IdempotencyKey, nil, &out)
	return out, err
}

// GetCharge fetches a charge by id.
func (c *Client) GetCharge(ctx context.Context, chargeID string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/charges/"+url.PathEscape(chargeID), nil, "", nil, &out)
	return out, err
}

// PayoutParams describes sending money out of the platform balance.
type PayoutParams struct {
	AmountKes      float64
	Wallet         string
	ChannelID      string
	DestinationType string // mobile | till | paybill | bank
	Phone          string
	ShortCode      string
	AccountRef     string
	BankCode       string
	AccountNumber  string
	AccountName    string
	IdempotencyKey string
}

// CreatePayout sends money to a mobile/till/paybill/bank destination.
func (c *Client) CreatePayout(ctx context.Context, p PayoutParams) (map[string]interface{}, error) {
	cents, err := ToCents(p.AmountKes)
	if err != nil {
		return nil, err
	}
	if cents < PayoutMinCents {
		return nil, &Error{Message: "the smallest payout is KES 30"}
	}
	body := map[string]interface{}{"amount": cents}
	if p.Wallet != "" {
		body["wallet"] = p.Wallet
	}
	if p.ChannelID != "" {
		body["channel_id"] = p.ChannelID
	} else {
		t := orDefault(p.DestinationType, "mobile")
		body["destination_type"] = t
		switch t {
		case "mobile":
			body["phone"] = orDefault(NormalizePhone(p.Phone), p.Phone)
		case "till":
			body["short_code"] = p.ShortCode
		case "paybill":
			body["short_code"] = p.ShortCode
			body["account_ref"] = p.AccountRef
		case "bank":
			body["bank_code"] = p.BankCode
			body["account_number"] = p.AccountNumber
			body["account_name"] = p.AccountName
		}
	}
	out := map[string]interface{}{}
	err = c.do(ctx, http.MethodPost, "/payouts", body, p.IdempotencyKey, nil, &out)
	return out, err
}

// GetWallets returns balances (in cents) for collections, collections_pending and payouts.
func (c *Client) GetWallets(ctx context.Context) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/wallets", nil, "", nil, &out)
	return out, err
}

// ListPayoutBanks returns the banks the rail can pay.
func (c *Client) ListPayoutBanks(ctx context.Context) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/payout-banks", nil, "", nil, &out)
	return out, err
}

// VerifyTransaction verifies a hosted-checkout transaction by reference.
func (c *Client) VerifyTransaction(ctx context.Context, reference string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/transactions/verify/"+url.PathEscape(reference), nil, "", nil, &out)
	return out, err
}

// VerifyWebhookSignature checks X-Cyzora-Signature (hex HMAC-SHA256 of the raw body)
// against every configured webhook secret, using a constant-time comparison.
func (c *Client) VerifyWebhookSignature(rawBody []byte, signature string) bool {
	if len(c.webhookSecrets) == 0 || signature == "" || len(rawBody) == 0 {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return false
	}
	for _, secret := range c.webhookSecrets {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(rawBody)
		if hmac.Equal(mac.Sum(nil), sig) {
			return true
		}
	}
	return false
}

// ConstructWebhookEvent verifies the signature (against every configured secret)
// and parses the body in one call. It returns an error if the signature is invalid
// or the body is not JSON. rawBody must be the exact bytes received.
func (c *Client) ConstructWebhookEvent(rawBody []byte, signature string) (map[string]interface{}, error) {
	if !c.VerifyWebhookSignature(rawBody, signature) {
		return nil, &Error{Message: "webhook signature verification failed", Status: 401}
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rawBody, &out); err != nil {
		return nil, &Error{Message: "webhook body is not valid JSON", Status: 400}
	}
	return out, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
