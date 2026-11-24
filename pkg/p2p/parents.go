package p2p

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
)

// ErrParentNotFound means WhatsOnChain does not know the transaction.
var ErrParentNotFound = errors.New("parent transaction not found")

// ErrBudgetExceeded means one incoming request needed more WhatsOnChain
// requests or bytes than its Budget allows. The input is too big to verify
// here, not a fault of ours, so a retry cannot help.
var ErrBudgetExceeded = errors.New("ancestry too large to fetch")

// Budget bounds what one incoming request may cost WhatsOnChain: a count of
// requests, a count of response bytes, and a memo so that a txid reached by
// many inputs, or along many paths, is fetched once. Without it an attacker
// names real unmined or large transactions as inputs and turns one call into
// an exponential number of requests on the shared 3 a second limiter.
type Budget struct {
	mu          sync.Mutex
	requests    int
	bytes       int
	maxRequests int
	maxBytes    int
	memo        map[string]*transaction.Transaction
}

// NewBudget returns a Budget allowing maxRequests requests and maxBytes bytes.
func NewBudget(maxRequests, maxBytes int) *Budget {
	return &Budget{maxRequests: maxRequests, maxBytes: maxBytes, memo: map[string]*transaction.Transaction{}}
}

// Defaults for a Parent call whose context carries no Budget.
const (
	defaultMaxRequests = 50
	defaultMaxBytes    = 8 << 20
)

type budgetKey struct{}

// WithBudget returns a context whose Parent calls share b.
func WithBudget(ctx context.Context, b *Budget) context.Context {
	return context.WithValue(ctx, budgetKey{}, b)
}

func budgetFrom(ctx context.Context) *Budget {
	b, _ := ctx.Value(budgetKey{}).(*Budget)
	return b
}

// spend takes one request from the budget, before it is sent.
func (b *Budget) spend() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.requests >= b.maxRequests {
		return fmt.Errorf("%w: more than %d requests", ErrBudgetExceeded, b.maxRequests)
	}
	b.requests++
	return nil
}

// addBytes counts a response against the budget.
func (b *Budget) addBytes(n int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bytes += n
	if b.bytes > b.maxBytes {
		return fmt.Errorf("%w: more than %d bytes", ErrBudgetExceeded, b.maxBytes)
	}
	return nil
}

func (b *Budget) recall(txid string) *transaction.Transaction {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.memo[txid]
}

func (b *Budget) remember(txid string, tx *transaction.Transaction) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.memo[txid] = tx
}

const (
	wocBase        = "https://api.whatsonchain.com/v1/bsv/main"
	maxParentDepth = 10
	// WhatsOnChain allows 3 requests a second without a key.
	wocMinInterval = 334 * time.Millisecond
	maxWOCBody     = 4 << 20
)

type whatsOnChain struct {
	apiKey string
	client *http.Client
	base   string // overridable in tests

	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

// NewWhatsOnChain returns a ParentFetcher for mainnet. apiKey may be empty. A
// nil client gets one with a 15 second timeout.
func NewWhatsOnChain(apiKey string, client *http.Client) ParentFetcher {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &whatsOnChain{apiKey: apiKey, client: client, base: wocBase, interval: wocMinInterval}
}

// Parent returns the transaction with its proof when mined. A transaction that
// is not mined comes back with its own parents attached, up to depth 10, so a
// caller can SPV-verify it. The walk is bounded by the Budget in ctx (WithBudget),
// or by a default one, and fetches each txid once.
func (w *whatsOnChain) Parent(ctx context.Context, txid string) (*transaction.Transaction, error) {
	if budgetFrom(ctx) == nil {
		ctx = WithBudget(ctx, NewBudget(defaultMaxRequests, defaultMaxBytes))
	}
	return w.fetch(ctx, txid, 0)
}

func (w *whatsOnChain) fetch(ctx context.Context, txid string, depth int) (*transaction.Transaction, error) {
	if depth > maxParentDepth {
		return nil, fmt.Errorf("unmined ancestry of %s is deeper than %d", txid, maxParentDepth)
	}
	budget := budgetFrom(ctx)
	if tx := budget.recall(txid); tx != nil {
		return tx, nil
	}
	body, status, err := w.get(ctx, "/tx/"+txid+"/beef")
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusOK:
		tx, err := transaction.NewTransactionFromBEEFHex(strings.TrimSpace(string(body)))
		if err != nil {
			return nil, fmt.Errorf("parse beef for %s: %w", txid, err)
		}
		budget.remember(txid, tx)
		return tx, nil
	case status != http.StatusNotFound:
		return nil, fmt.Errorf("whatsonchain beef %s: status %d", txid, status)
	}

	// No BEEF: the transaction is not mined. Take the raw form and walk up.
	body, status, err = w.get(ctx, "/tx/"+txid+"/hex")
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", ErrParentNotFound, txid)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("whatsonchain hex %s: status %d", txid, status)
	}
	tx, err := transaction.NewTransactionFromHex(strings.TrimSpace(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parse tx %s: %w", txid, err)
	}
	for _, in := range tx.Inputs {
		src, err := w.fetch(ctx, in.SourceTXID.String(), depth+1)
		if err != nil {
			return nil, err
		}
		in.SourceTransaction = src
	}
	budget.remember(txid, tx)
	return tx, nil
}

// get waits for the rate limit, then GETs base+path.
func (w *whatsOnChain) get(ctx context.Context, path string) ([]byte, int, error) {
	budget := budgetFrom(ctx)
	if err := budget.spend(); err != nil {
		return nil, 0, err
	}
	if err := w.wait(ctx); err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.base+path, nil)
	if err != nil {
		return nil, 0, err
	}
	if w.apiKey != "" {
		req.Header.Set("woc-token", w.apiKey)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxWOCBody))
	if err != nil {
		return nil, 0, err
	}
	if err := budget.addBytes(len(b)); err != nil {
		return nil, 0, err
	}
	return b, resp.StatusCode, nil
}

// wait spaces requests at least w.interval apart.
func (w *whatsOnChain) wait(ctx context.Context) error {
	w.mu.Lock()
	now := time.Now()
	at := w.next
	if at.Before(now) {
		at = now
	}
	w.next = at.Add(w.interval)
	w.mu.Unlock()
	d := at.Sub(now)
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
