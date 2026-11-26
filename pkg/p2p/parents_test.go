package p2p

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/stretchr/testify/require"
)

func wocFake(t *testing.T, h http.HandlerFunc) (*whatsOnChain, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	w := NewWhatsOnChain("", srv.Client()).(*whatsOnChain)
	w.base = srv.URL
	w.interval = 0
	return w, srv
}

func TestWhatsOnChainBeef(t *testing.T) {
	var gotPath, gotToken string
	w, _ := wocFake(t, func(rw http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.Path, r.Header.Get("woc-token")
		_, _ = rw.Write([]byte(minedBeefHex(t) + "\n"))
	})
	w.apiKey = "tok"
	tx, err := w.Parent(context.Background(), minedTxid)
	require.NoError(t, err)
	require.Equal(t, minedTxid, tx.TxID().String())
	require.NotNil(t, tx.MerklePath)
	require.Equal(t, "/tx/"+minedTxid+"/beef", gotPath)
	require.Equal(t, "tok", gotToken)
}

func TestWhatsOnChainNoKeyNoHeader(t *testing.T) {
	var gotToken []string
	w, _ := wocFake(t, func(rw http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Values("woc-token")
		_, _ = rw.Write([]byte(minedBeefHex(t)))
	})
	_, err := w.Parent(context.Background(), minedTxid)
	require.NoError(t, err)
	require.Empty(t, gotToken)
}

func TestWhatsOnChainUnminedWalksParents(t *testing.T) {
	mined := minedTx(t)
	raw := mined.Hex()
	parent := mined.Inputs[0].SourceTXID.String()
	other := mined.Inputs[1].SourceTXID.String()
	var paths []string
	var mu sync.Mutex
	w, _ := wocFake(t, func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/tx/" + minedTxid + "/beef":
			rw.WriteHeader(404)
		case "/tx/" + minedTxid + "/hex":
			_, _ = rw.Write([]byte(raw))
		case "/tx/" + parent + "/beef", "/tx/" + other + "/beef":
			_, _ = rw.Write([]byte(minedBeefHex(t))) // any mined tx stands in as the parent
		default:
			rw.WriteHeader(500)
		}
	})
	tx, err := w.Parent(context.Background(), minedTxid)
	require.NoError(t, err)
	require.Len(t, tx.Inputs, 2)
	for _, in := range tx.Inputs {
		require.NotNil(t, in.SourceTransaction)
	}
}

func TestWhatsOnChainDepthLimit(t *testing.T) {
	// Every transaction claims to be unmined and to spend itself, forever.
	raw := minedTx(t).Hex()
	w, _ := wocFake(t, func(rw http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) > 4 && r.URL.Path[len(r.URL.Path)-4:] == "/hex" {
			_, _ = rw.Write([]byte(raw))
			return
		}
		rw.WriteHeader(404)
	})
	_, err := w.Parent(context.Background(), minedTxid)
	require.ErrorContains(t, err, "deeper than")
}

func TestWhatsOnChainNotFound(t *testing.T) {
	w, _ := wocFake(t, func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(404) })
	_, err := w.Parent(context.Background(), minedTxid)
	require.ErrorIs(t, err, ErrParentNotFound)
}

func TestWhatsOnChainServerError(t *testing.T) {
	w, _ := wocFake(t, func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(502) })
	_, err := w.Parent(context.Background(), minedTxid)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrParentNotFound)
}

func TestWhatsOnChainRateLimit(t *testing.T) {
	w, _ := wocFake(t, func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(minedBeefHex(t)))
	})
	w.interval = 60 * time.Millisecond
	start := time.Now()
	for i := 0; i < 3; i++ {
		_, err := w.Parent(context.Background(), minedTxid)
		require.NoError(t, err)
	}
	require.GreaterOrEqual(t, time.Since(start), 110*time.Millisecond)
}

// diamond serves an unmined top transaction whose n inputs all spend one
// unmined ancestor, which spends two mined transactions. It returns the top's
// txid and a counter of the requests the fake received.
func diamond(t *testing.T, n int) (*whatsOnChain, string, *atomic.Int64) {
	t.Helper()
	zero := func(b byte) *chainhash.Hash { h := chainhash.Hash{}; h[0] = b; return &h }
	spend := func(tx *transaction.Transaction, src *chainhash.Hash, vout uint32) {
		tx.AddInput(&transaction.TransactionInput{
			SourceTXID: src, SourceTxOutIndex: vout,
			UnlockingScript: &script.Script{}, SequenceNumber: transaction.DefaultSequenceNumber,
		})
	}
	ancestor := transaction.NewTransaction()
	spend(ancestor, zero(1), 0)
	spend(ancestor, zero(2), 0)
	ancestor.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: &script.Script{}})
	ancestorID := ancestor.TxID()
	top := transaction.NewTransaction()
	for i := 0; i < n; i++ {
		spend(top, ancestorID, uint32(i))
	}
	top.AddOutput(&transaction.TransactionOutput{Satoshis: 1, LockingScript: &script.Script{}})

	hexes := map[string]string{top.TxID().String(): top.Hex(), ancestorID.String(): ancestor.Hex()}
	var reqs atomic.Int64
	w, _ := wocFake(t, func(rw http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // tx/<id>/<kind>
		id, kind := parts[1], parts[2]
		if h, ok := hexes[id]; ok {
			if kind == "hex" {
				_, _ = rw.Write([]byte(h))
				return
			}
			rw.WriteHeader(404) // unmined: no BEEF
			return
		}
		_, _ = rw.Write([]byte(minedBeefHex(t))) // the two mined leaves
	})
	return w, top.TxID().String(), &reqs
}

// Without memoization the walk fetches the shared ancestor once per input, and
// across levels the cost multiplies. Each txid must be fetched once.
func TestWhatsOnChainSharedAncestorFetchedOnce(t *testing.T) {
	w, top, reqs := diamond(t, 20)
	tx, err := w.Parent(context.Background(), top)
	require.NoError(t, err)
	require.Len(t, tx.Inputs, 20)
	require.Same(t, tx.Inputs[0].SourceTransaction, tx.Inputs[19].SourceTransaction, "one *Transaction per txid")
	// top (beef+hex), ancestor (beef+hex), two leaves (beef each).
	require.LessOrEqual(t, reqs.Load(), int64(6))
}

func TestWhatsOnChainRequestBudget(t *testing.T) {
	w, top, reqs := diamond(t, 20)
	ctx := WithBudget(context.Background(), NewBudget(3, 1<<20))
	_, err := w.Parent(ctx, top)
	require.ErrorIs(t, err, ErrBudgetExceeded)
	require.LessOrEqual(t, reqs.Load(), int64(3), "no request beyond the budget may be sent")
}

func TestWhatsOnChainByteBudget(t *testing.T) {
	w, top, _ := diamond(t, 20)
	ctx := WithBudget(context.Background(), NewBudget(100, 100))
	_, err := w.Parent(ctx, top)
	require.ErrorIs(t, err, ErrBudgetExceeded)
}

// The budget is shared by every Parent call of one request, so many distinct
// parents cannot each get a fresh allowance.
func TestWhatsOnChainBudgetSpansCalls(t *testing.T) {
	w, _ := wocFake(t, func(rw http.ResponseWriter, _ *http.Request) { _, _ = rw.Write([]byte(minedBeefHex(t))) })
	ctx := WithBudget(context.Background(), NewBudget(2, 1<<20))
	for _, id := range []string{"01", "02"} {
		_, err := w.Parent(ctx, id+minedTxid[2:])
		require.NoError(t, err)
	}
	_, err := w.Parent(ctx, "03"+minedTxid[2:])
	require.ErrorIs(t, err, ErrBudgetExceeded)
}
