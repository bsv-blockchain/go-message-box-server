package p2p

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/stretchr/testify/require"
)

const minedTxid = "15bed688d2f5e7db96c3763d35fb827400cbe77a06ae865fda47e0a0b8e9867f"

// Bodies recorded from Arcade (see the comment at the top of broadcast.go).
const (
	arcadeReceived = `{"txid":"` + minedTxid + `","status":202,"txStatus":"RECEIVED"}`
	arcadeNotEF    = `{"detail":"Missing input scripts: Transaction could not be transformed to extended format","error":"transaction failed validation","reason":"transaction could not be transformed to extended format: missing input source data: input 0","status":460,"title":"Not extended format","txid":"` + minedTxid + `","type":"https://bitcoin-sv.github.io/arc/#/errors?id=_460"}`
)

func minedBeefHex(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/mined_beef.hex")
	require.NoError(t, err)
	return strings.TrimSpace(string(b))
}

func minedTx(t *testing.T) *transaction.Transaction {
	t.Helper()
	tx, err := transaction.NewTransactionFromBEEFHex(minedBeefHex(t))
	require.NoError(t, err)
	require.Equal(t, minedTxid, tx.TxID().String())
	return tx
}

func arcadeFake(t *testing.T, status int, body string, seen *http.Request) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = *r
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestArcadeAcceptedAndDuplicate(t *testing.T) {
	// Arcade answers a first submission and a duplicate with the same 202.
	srv := arcadeFake(t, 202, arcadeReceived, nil)
	defer srv.Close()
	b := NewArcade(srv.URL, "", srv.Client())
	for i := 0; i < 2; i++ {
		txid, err := b.Broadcast(context.Background(), minedTx(t))
		require.NoError(t, err)
		require.Equal(t, minedTxid, txid)
	}
}

func TestArcadeSeenOnNetwork(t *testing.T) {
	srv := arcadeFake(t, 200, `{"txid":"`+minedTxid+`","txStatus":"SEEN_ON_NETWORK"}`, nil)
	defer srv.Close()
	txid, err := NewArcade(srv.URL, "", srv.Client()).Broadcast(context.Background(), minedTx(t))
	require.NoError(t, err)
	require.Equal(t, minedTxid, txid)
}

func TestArcadeAlreadyKnownTextIsSuccess(t *testing.T) {
	srv := arcadeFake(t, 409, `{"detail":"txn-already-known","status":409,"title":"Conflict"}`, nil)
	defer srv.Close()
	txid, err := NewArcade(srv.URL, "", srv.Client()).Broadcast(context.Background(), minedTx(t))
	require.NoError(t, err)
	require.Equal(t, minedTxid, txid)
}

func TestArcadeRejection(t *testing.T) {
	srv := arcadeFake(t, 400, arcadeNotEF, nil)
	defer srv.Close()
	_, err := NewArcade(srv.URL, "", srv.Client()).Broadcast(context.Background(), minedTx(t))
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "Missing input scripts")
}

func TestArcadeRejectedStatusOn200(t *testing.T) {
	srv := arcadeFake(t, 200, `{"txid":"`+minedTxid+`","txStatus":"DOUBLE_SPEND_ATTEMPTED","extraInfo":"competing"}`, nil)
	defer srv.Close()
	_, err := NewArcade(srv.URL, "", srv.Client()).Broadcast(context.Background(), minedTx(t))
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "competing")
}

func TestArcadeServerErrorIsNotRejection(t *testing.T) {
	srv := arcadeFake(t, 503, `{"detail":"busy"}`, nil)
	defer srv.Close()
	_, err := NewArcade(srv.URL, "", srv.Client()).Broadcast(context.Background(), minedTx(t))
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRejected)
}

func TestArcadeTimeoutIsNotRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	b := NewArcade(srv.URL, "", &http.Client{Timeout: 30 * time.Millisecond})
	_, err := b.Broadcast(context.Background(), minedTx(t))
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRejected)
}

func TestArcadeRequestShape(t *testing.T) {
	var with, without http.Request
	s1 := arcadeFake(t, 202, arcadeReceived, &with)
	defer s1.Close()
	s2 := arcadeFake(t, 202, arcadeReceived, &without)
	defer s2.Close()

	_, err := NewArcade(s1.URL+"/", "k3y", s1.Client()).Broadcast(context.Background(), minedTx(t))
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, with.Method)
	require.Equal(t, "/tx", with.URL.Path)
	require.Equal(t, "Bearer k3y", with.Header.Get("Authorization"))

	_, err = NewArcade(s2.URL, "", s2.Client()).Broadcast(context.Background(), minedTx(t))
	require.NoError(t, err)
	require.Equal(t, "/tx", without.URL.Path)
	require.Empty(t, without.Header.Values("Authorization"))
}

// Only a verdict on the transaction is final. A 4xx that says our key, our
// quota or our timing is wrong says nothing about the transaction, so the
// sender must be told to retry (a 5xx), not that the payment is dead.
func TestArcadeOurTroubleIsNotRejection(t *testing.T) {
	for _, status := range []int{401, 403, 404, 408, 425, 429} {
		srv := arcadeFake(t, status, `{"detail":"nope"}`, nil)
		_, err := NewArcade(srv.URL, "", srv.Client()).Broadcast(context.Background(), minedTx(t))
		srv.Close()
		require.Error(t, err, status)
		require.NotErrorIs(t, err, ErrRejected, "status %d", status)
	}
}

func TestArcadeTerminalVerdictsAreRejection(t *testing.T) {
	for _, status := range []int{400, 409, 422, 460, 461, 463, 469} {
		srv := arcadeFake(t, status, `{"detail":"bad tx"}`, nil)
		_, err := NewArcade(srv.URL, "", srv.Client()).Broadcast(context.Background(), minedTx(t))
		srv.Close()
		require.ErrorIs(t, err, ErrRejected, "status %d", status)
	}
}
