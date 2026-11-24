package p2p

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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
	w, _ := wocFake(t, func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(404) })
	_, err := w.Parent(context.Background(), minedTxid)
	require.ErrorIs(t, err, ErrParentNotFound)
}

func TestWhatsOnChainServerError(t *testing.T) {
	w, _ := wocFake(t, func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(502) })
	_, err := w.Parent(context.Background(), minedTxid)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrParentNotFound)
}

func TestWhatsOnChainRateLimit(t *testing.T) {
	w, _ := wocFake(t, func(rw http.ResponseWriter, r *http.Request) {
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
