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
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://api.cyzora.co.ke/api/v1"
	PayoutMinCents = 3000 // KES 30
	// MetadataMaxKeys is the most metadata keys the rail accepts on a charge or checkout session.
	MetadataMaxKeys = 20
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
	EventWebhookTest     = "webhook.test"
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

// New builds a Cyzora client. secretKey is the sk_live_... key. Base URL and
// webhook secrets default to CYZORA_API_BASE and CYZORA_WEBHOOK_SECRET.
func New(secretKey string, opts ...Option) *Client {
	c := &Client{
		secretKey:  secretKey,
		baseURL:    DefaultBaseURL,
		http:       &http.Client{Timeout: 30 * time.Second},
		maxRetries: 2,
	}
	if base := os.Getenv("CYZORA_API_BASE"); base != "" {
		c.baseURL = strings.TrimRight(base, "/")
	}
	for _, secret := range strings.Split(os.Getenv("CYZORA_WEBHOOK_SECRET"), ",") {
		if secret = strings.TrimSpace(secret); secret != "" {
			c.webhookSecrets = append(c.webhookSecrets, secret)
		}
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

// ToCents converts shillings to cents, rounding up to the next whole shilling as
// the rail does for charges and checkout sessions.
func ToCents(amountKes float64) (int64, error) {
	if math.IsNaN(amountKes) || math.IsInf(amountKes, 0) {
		return 0, &Error{Message: "amount must be a number"}
	}
	kes := int64(math.Ceil(amountKes))
	if kes < 1 {
		return 0, &Error{Message: "amount must be at least KES 1"}
	}
	return kes * 100, nil
}

// payoutCents rejects fractional shillings, which the rail refuses for payouts.
func payoutCents(amountKes float64, amountCents int64) (int64, error) {
	cents := amountCents
	if cents == 0 {
		if math.IsNaN(amountKes) || math.IsInf(amountKes, 0) {
			return 0, &Error{Message: "amount must be a number"}
		}
		cents = int64(amountKes * 100)
	}
	if cents%100 != 0 {
		return 0, &Error{Message: "payout amounts must be whole shillings (a multiple of 100 cents)"}
	}
	if cents < 100 {
		return 0, &Error{Message: "amount must be at least KES 1"}
	}
	return cents, nil
}

// NormalizeMetadata converts metadata values to strings. The rail stores
// metadata as string pairs and refuses the whole request (HTTP 400) if a value
// is a number, boolean or object. Nil values are dropped; maps and slices
// become JSON; at most MetadataMaxKeys keys.
func NormalizeMetadata(metadata map[string]interface{}) (map[string]string, error) {
	if len(metadata) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		switch t := v.(type) {
		case nil:
			continue
		case string:
			out[k] = t
		case fmt.Stringer:
			out[k] = t.String()
		case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
			out[k] = fmt.Sprint(t)
		default:
			b, err := json.Marshal(t)
			if err != nil {
				return nil, &Error{Message: fmt.Sprintf("metadata %q could not be encoded: %v", k, err)}
			}
			out[k] = string(b)
		}
	}
	if len(out) > MetadataMaxKeys {
		return nil, &Error{Message: fmt.Sprintf("metadata can hold at most %d keys (got %d)", MetadataMaxKeys, len(out))}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// moveCents gives exact cents for wallet moves: whole cents, greater than zero.
func moveCents(amountKes float64, amountCents int64) (int64, error) {
	cents := amountCents
	if cents == 0 {
		if math.IsNaN(amountKes) || math.IsInf(amountKes, 0) {
			return 0, &Error{Message: "amount must be a number"}
		}
		cents = int64(math.Round(amountKes * 100))
	}
	if cents <= 0 {
		return 0, &Error{Message: "amount must be greater than zero"}
	}
	return cents, nil
}

// FromCents converts integer cents back to shillings.
func FromCents(cents int64) float64 { return float64(cents) / 100 }

func (c *Client) do(ctx context.Context, method, path string, body interface{}, idempotencyKey string, query map[string]string, out interface{}, auth bool) error {
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
		if auth {
			req.Header.Set("X-API-Key", c.secretKey)
		}
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
			// A 409 with an idempotency key means the earlier request is still
			// being processed; the docs advise waiting and retrying with it.
			transient := resp.StatusCode >= 500 || resp.StatusCode == 429 || (resp.StatusCode == 409 && idempotencyKey != "")
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
			// CSV exports are text, not JSON.
			if text, ok := out.(*string); ok {
				*text = string(raw)
				return nil
			}
			return json.Unmarshal(raw, out)
		}
		return nil
	}
}

// Request is an escape hatch for any endpoint, including new ones the rail adds
// before this SDK ships a typed wrapper. path is appended to the base URL.
func (c *Client) Request(ctx context.Context, method, path string, body interface{}, idempotencyKey string, query map[string]string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, method, path, body, idempotencyKey, query, &out, true)
	return out, err
}

// RequestInto is like Request but decodes into out, so endpoints that return a
// JSON array (or a struct you define) work too: pass a pointer such as
// &[]map[string]interface{}{} or *string for raw text.
func (c *Client) RequestInto(ctx context.Context, method, path string, body interface{}, idempotencyKey string, query map[string]string, out interface{}) error {
	return c.do(ctx, method, path, body, idempotencyKey, query, out, true)
}

// PublicRequest calls an endpoint that needs no secret key, such as statement
// verification. It follows the same retry and parsing rules as Request.
func (c *Client) PublicRequest(ctx context.Context, method, path string, query map[string]string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, method, path, nil, "", query, &out, false)
	return out, err
}

// ChargeParams describes an STK push.
type ChargeParams struct {
	AmountKes   float64
	AmountCents int64
	Phone       string
	Network     string
	AccountRef  string
	// Deprecated: the rail does not store a description on STK charges; it is no longer sent.
	Description string
	// Metadata holds up to 20 keys; values are sent as strings.
	Metadata       map[string]interface{}
	IdempotencyKey string
}

// CreateCharge sends an M-Pesa STK prompt to a customer's phone.
func (c *Client) CreateCharge(ctx context.Context, p ChargeParams) (map[string]interface{}, error) {
	phone := NormalizePhone(p.Phone)
	if phone == "" {
		return nil, &Error{Message: "enter a valid M-Pesa phone number (e.g. 0712 345 678)"}
	}
	cents := p.AmountCents
	if cents == 0 {
		var err error
		if cents, err = ToCents(p.AmountKes); err != nil {
			return nil, err
		}
	} else {
		cents = ((cents + 99) / 100) * 100 // the rail rounds odd cents up for charges
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
	meta, err := NormalizeMetadata(p.Metadata)
	if err != nil {
		return nil, err
	}
	if meta != nil {
		body["metadata"] = meta
	}
	out := map[string]interface{}{}
	err = c.do(ctx, http.MethodPost, "/charges", body, p.IdempotencyKey, nil, &out, true)
	return out, err
}

// ListChargesParams filters ListCharges. Dates look like 2026-10-01; Mode is
// live (default), test or all. A test key only ever sees test charges.
type ListChargesParams struct {
	Status   string
	FromDate string
	ToDate   string
	Mode     string
}

// ListCharges lists charges, newest first.
func (c *Client) ListCharges(ctx context.Context, p ListChargesParams) ([]map[string]interface{}, error) {
	out := []map[string]interface{}{}
	query := map[string]string{"status": p.Status, "from_date": p.FromDate, "to_date": p.ToDate, "mode": p.Mode}
	err := c.do(ctx, http.MethodGet, "/charges", nil, "", query, &out, true)
	return out, err
}

// ExportCharges downloads charges as CSV; from/to are dates such as 2026-10-01.
func (c *Client) ExportCharges(ctx context.Context, query map[string]string) (string, error) {
	out := ""
	err := c.do(ctx, http.MethodGet, "/charges/export", nil, "", query, &out, true)
	return out, err
}

// GetCharge fetches a charge by id.
func (c *Client) GetCharge(ctx context.Context, chargeID string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/charges/"+url.PathEscape(chargeID), nil, "", nil, &out, true)
	return out, err
}

// CheckoutParams describes a hosted checkout session.
type CheckoutParams struct {
	AmountKes      float64
	AccountRef     string
	Description    string
	CallbackURL    string
	CancelURL      string
	CustomerPhone  string
	CustomerEmail  string
	Metadata       map[string]interface{}
	ExpiresIn      int
	IdempotencyKey string
}

// CreateCheckoutSession starts a hosted checkout session on the rail.
func (c *Client) CreateCheckoutSession(ctx context.Context, p CheckoutParams) (map[string]interface{}, error) {
	cents, err := ToCents(p.AmountKes)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{"amount": cents}
	if p.AccountRef != "" {
		body["account_ref"] = trunc(p.AccountRef, 64)
	}
	if p.Description != "" {
		body["description"] = trunc(p.Description, 200)
	}
	if p.CallbackURL != "" {
		body["callback_url"] = p.CallbackURL
	}
	if p.CancelURL != "" {
		body["cancel_url"] = p.CancelURL
	}
	if p.CustomerPhone != "" {
		body["customer_phone"] = orDefault(NormalizePhone(p.CustomerPhone), p.CustomerPhone)
	}
	if p.CustomerEmail != "" {
		body["customer_email"] = p.CustomerEmail
	}
	meta, err := NormalizeMetadata(p.Metadata)
	if err != nil {
		return nil, err
	}
	if meta != nil {
		body["metadata"] = meta
	}
	if p.ExpiresIn != 0 {
		body["expires_in"] = p.ExpiresIn
	}
	out := map[string]interface{}{}
	err = c.do(ctx, http.MethodPost, "/transactions/initialize", body, p.IdempotencyKey, nil, &out, true)
	return out, err
}

// PayoutParams describes sending money out of the platform balance.
type PayoutParams struct {
	AmountKes       float64
	AmountCents     int64
	Wallet          string
	ChannelID       string
	DestinationType string // mobile | till | paybill | bank
	Phone           string
	ShortCode       string
	AccountRef      string
	BankCode        string
	AccountNumber   string
	AccountName     string
	IdempotencyKey  string
}

// CreatePayout sends money to a mobile/till/paybill/bank destination.
func (c *Client) CreatePayout(ctx context.Context, p PayoutParams) (map[string]interface{}, error) {
	cents, err := payoutCents(p.AmountKes, p.AmountCents)
	if err != nil {
		return nil, err
	}
	if cents < PayoutMinCents {
		return nil, &Error{Message: "the smallest payout is KES 30"}
	}
	body := map[string]interface{}{"amount": cents}
	wallet := p.Wallet
	if wallet == "" {
		wallet = os.Getenv("CYZORA_PAYOUT_WALLET")
	}
	if wallet != "" {
		body["wallet"] = wallet
	}
	if p.ChannelID != "" {
		body["channel_id"] = p.ChannelID
	} else {
		t := orDefault(p.DestinationType, "mobile")
		body["destination_type"] = t
		switch t {
		case "mobile":
			phone := NormalizePhone(p.Phone)
			if phone == "" {
				return nil, &Error{Message: "phone must be a valid M-Pesa number for mobile payouts"}
			}
			body["phone"] = phone
		case "till":
			if p.ShortCode == "" {
				return nil, &Error{Message: "short_code is required for till payouts"}
			}
			body["short_code"] = p.ShortCode
		case "paybill":
			if p.ShortCode == "" || p.AccountRef == "" {
				return nil, &Error{Message: "short_code and account_ref are required for paybill payouts"}
			}
			body["short_code"] = p.ShortCode
			body["account_ref"] = p.AccountRef
		case "bank":
			if p.BankCode == "" || p.AccountNumber == "" || p.AccountName == "" {
				return nil, &Error{Message: "bank_code, account_number and account_name are required for bank payouts"}
			}
			body["bank_code"] = p.BankCode
			body["account_number"] = p.AccountNumber
			body["account_name"] = p.AccountName
		default:
			return nil, &Error{Message: "destination_type must be mobile, till, paybill or bank"}
		}
	}
	out := map[string]interface{}{}
	err = c.do(ctx, http.MethodPost, "/payouts", body, p.IdempotencyKey, nil, &out, true)
	return out, err
}

// GetPayout fetches a payout by id.
func (c *Client) GetPayout(ctx context.Context, payoutID string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/payouts/"+url.PathEscape(payoutID), nil, "", nil, &out, true)
	return out, err
}

// ListPayouts lists payouts, newest first. The rail returns a JSON array and
// does not filter this list yet; query is forwarded as-is.
func (c *Client) ListPayouts(ctx context.Context, query map[string]string) ([]map[string]interface{}, error) {
	out := []map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/payouts", nil, "", query, &out, true)
	return out, err
}

// ListPayoutBatches lists bulk payout batches (created in the dashboard; read-only by API key).
func (c *Client) ListPayoutBatches(ctx context.Context) ([]map[string]interface{}, error) {
	out := []map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/payouts/batches", nil, "", nil, &out, true)
	return out, err
}

// GetPayoutBatch fetches one bulk payout batch with its rows.
func (c *Client) GetPayoutBatch(ctx context.Context, batchID string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/payouts/batches/"+url.PathEscape(batchID), nil, "", nil, &out, true)
	return out, err
}

// PreviewWalletMove previews moving money from Collections into the Payouts wallet.
// Pass amountCents to give exact cents instead of shillings.
func (c *Client) PreviewWalletMove(ctx context.Context, amountKes float64, amountCents int64) (map[string]interface{}, error) {
	cents, err := moveCents(amountKes, amountCents)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	err = c.do(ctx, http.MethodGet, "/wallets/move/preview", nil, "", map[string]string{"amount": fmt.Sprint(cents)}, &out, true)
	return out, err
}

// WalletMoveParams describes a Collections -> Payouts move.
type WalletMoveParams struct {
	AmountKes      float64
	AmountCents    int64
	IdempotencyKey string
}

// MoveToPayouts moves money from Collections into the Payouts wallet. Live keys only.
func (c *Client) MoveToPayouts(ctx context.Context, p WalletMoveParams) (map[string]interface{}, error) {
	cents, err := moveCents(p.AmountKes, p.AmountCents)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	err = c.do(ctx, http.MethodPost, "/wallets/move", map[string]interface{}{"amount": cents}, p.IdempotencyKey, nil, &out, true)
	return out, err
}

// ListWalletTransfers lists past Collections -> Payouts moves ({"data": [...]}). limit 0 uses the rail default.
func (c *Client) ListWalletTransfers(ctx context.Context, limit int) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/wallets/transfers", nil, "", limitQuery(limit), &out, true)
	return out, err
}

// ListSubsidiaries lists subsidiary businesses this account can top up ({"data": [...]}).
func (c *Client) ListSubsidiaries(ctx context.Context) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/wallets/subsidiaries", nil, "", nil, &out, true)
	return out, err
}

// SubsidiaryTransferParams describes topping up a subsidiary's Payouts wallet.
// FromWallet is the parent's wallet the money leaves: collections or payouts.
type SubsidiaryTransferParams struct {
	SubsidiaryID   string
	AmountKes      float64
	AmountCents    int64
	FromWallet     string
	IdempotencyKey string
}

// PreviewSubsidiaryTransfer previews a subsidiary top-up. Live keys only.
func (c *Client) PreviewSubsidiaryTransfer(ctx context.Context, p SubsidiaryTransferParams) (map[string]interface{}, error) {
	if p.SubsidiaryID == "" {
		return nil, &Error{Message: "SubsidiaryID is required"}
	}
	cents, err := moveCents(p.AmountKes, p.AmountCents)
	if err != nil {
		return nil, err
	}
	query := map[string]string{"subsidiary_id": p.SubsidiaryID, "from_wallet": p.FromWallet, "amount": fmt.Sprint(cents)}
	out := map[string]interface{}{}
	err = c.do(ctx, http.MethodGet, "/wallets/subsidiary-transfers/preview", nil, "", query, &out, true)
	return out, err
}

// CreateSubsidiaryTransfer tops up a subsidiary's Payouts wallet from this account. Live keys only.
func (c *Client) CreateSubsidiaryTransfer(ctx context.Context, p SubsidiaryTransferParams) (map[string]interface{}, error) {
	if p.SubsidiaryID == "" {
		return nil, &Error{Message: "SubsidiaryID is required"}
	}
	cents, err := moveCents(p.AmountKes, p.AmountCents)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{"subsidiary_id": p.SubsidiaryID, "amount": cents}
	if p.FromWallet != "" {
		body["from_wallet"] = p.FromWallet
	}
	out := map[string]interface{}{}
	err = c.do(ctx, http.MethodPost, "/wallets/subsidiary-transfers", body, p.IdempotencyKey, nil, &out, true)
	return out, err
}

// ListSubsidiaryTransfers lists past subsidiary top-ups ({"data": [...]}). limit 0 uses the rail default.
func (c *Client) ListSubsidiaryTransfers(ctx context.Context, limit int) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/wallets/subsidiary-transfers", nil, "", limitQuery(limit), &out, true)
	return out, err
}

func limitQuery(limit int) map[string]string {
	if limit <= 0 {
		return nil
	}
	return map[string]string{"limit": fmt.Sprint(limit)}
}

// GetWallets returns balances (in cents) for collections, collections_pending and payouts.
func (c *Client) GetWallets(ctx context.Context) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/wallets", nil, "", nil, &out, true)
	return out, err
}

// ListPayoutChannels returns saved, verified payout destinations.
func (c *Client) ListPayoutChannels(ctx context.Context) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/payout-channels", nil, "", nil, &out, true)
	return out, err
}

// ListPayoutBanks returns the banks the rail can pay.
func (c *Client) ListPayoutBanks(ctx context.Context) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/payout-banks", nil, "", nil, &out, true)
	return out, err
}

// PayoutFeePreview previews the provider fee with the rail's `destination`
// parameter and the same wallet the payout will use.
func (c *Client) PayoutFeePreview(ctx context.Context, amountKes float64, destinationType, wallet string) (map[string]interface{}, error) {
	cents, err := payoutCents(amountKes, 0)
	if err != nil {
		return nil, err
	}
	query := map[string]string{
		"amount":      fmt.Sprintf("%d", cents),
		"destination": orDefault(destinationType, "mobile"),
		"wallet":      wallet,
	}
	out := map[string]interface{}{}
	err = c.do(ctx, http.MethodGet, "/payouts/fee-preview", nil, "", query, &out, true)
	return out, err
}

// VerifyTransaction verifies a hosted-checkout transaction by reference.
func (c *Client) VerifyTransaction(ctx context.Context, reference string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	err := c.do(ctx, http.MethodGet, "/transactions/verify/"+url.PathEscape(reference), nil, "", nil, &out, true)
	return out, err
}

// VerifyStatement verifies a 12-character statement code. This public endpoint
// needs no secret key.
func (c *Client) VerifyStatement(ctx context.Context, code string) (map[string]interface{}, error) {
	return c.PublicRequest(ctx, http.MethodGet, "/public/statements/verify/"+url.PathEscape(code), nil)
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
// or the body is not JSON. rawBody must be the exact bytes received. When an
// X-Cyzora-Event header is passed, it must agree with the event in the body.
func (c *Client) ConstructWebhookEvent(rawBody []byte, signature string, eventHeader ...string) (map[string]interface{}, error) {
	if !c.VerifyWebhookSignature(rawBody, signature) {
		return nil, &Error{Message: "webhook signature verification failed", Status: 401}
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rawBody, &out); err != nil {
		return nil, &Error{Message: "webhook body is not valid JSON", Status: 400}
	}
	if len(eventHeader) > 0 && eventHeader[0] != "" && out["event"] != eventHeader[0] {
		return nil, &Error{Message: "webhook event header does not match the body", Status: 400}
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
