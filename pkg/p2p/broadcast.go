package p2p

// Arcade's real answers, recorded 2026-11-24 against
// https://broadcast.bsvblockchain.tech with go-sdk v1.7.8 broadcaster.Arc and
// the mainnet transaction 15bed688d2f5e7db96c3763d35fb827400cbe77a06ae865fda47e0a0b8e9867f
// (already mined, so submitting it is harmless). The tests replay these bodies.
//
// Request: POST {url}/tx, Content-Type: application/json, body {"rawTx":"<hex>"}.
// Arc sends Extended Format when every input carries its source output, and
// plain raw hex otherwise.
//
//	Raw hex, parents missing:
//	  HTTP 400
//	  {"detail":"Missing input scripts: Transaction could not be transformed to extended format","error":"transaction failed validation","reason":"transaction could not be transformed to extended format: missing input source data: input 0","status":460,"title":"Not extended format","txid":"15be...867f","type":"https://bitcoin-sv.github.io/arc/#/errors?id=_460"}
//	  Broadcast -> failure code "400", description = detail.
//
//	EF, first submission:
//	  HTTP 202
//	  {"txid":"15be...867f","status":202,"txStatus":"RECEIVED"}
//	  Broadcast -> txid, nil.
//
//	EF, the same transaction submitted again:
//	  HTTP 202, the identical body.
//	  Broadcast -> txid, nil. Arcade is idempotent, so a retry or a transaction
//	  HandCash broadcast too needs no special case. Standard ARC answers a
//	  duplicate with HTTP 200 and an accepted txStatus such as SEEN_ON_NETWORK,
//	  which go-sdk already reports as success. So no text match is needed:
//	  failures are classified by code only, and response text never decides
//	  success.
//
// go-sdk Arc parses all of these, so no direct client is needed. The caller
// must attach each input's source transaction, or Arcade refuses with the 460
// above.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/broadcaster"
	"github.com/bsv-blockchain/go-sdk/util"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
)

// ErrRejected means the network refused the transaction (a 4xx, a double
// spend, an invalid script). It is final: retrying the same bytes cannot
// help. Every other Broadcast error is transient or ours, so the caller
// should answer 5xx and let the sender retry.
var ErrRejected = errors.New("transaction rejected")

// arcade is a Broadcaster backed by go-sdk's Arc client pointed at Arcade.
type arcade struct{ arc *broadcaster.Arc }

// NewArcade returns a Broadcaster for an Arcade (or ARC) server. apiKey may be
// empty. A nil client uses http.DefaultClient, which has no timeout, so
// callers should pass a client with one (or a context deadline).
func NewArcade(url, apiKey string, client util.HTTPClient) Broadcaster {
	if client == nil {
		client = http.DefaultClient
	}
	return &arcade{arc: &broadcaster.Arc{ApiUrl: strings.TrimRight(url, "/"), ApiKey: apiKey, Client: client}}
}

func (a *arcade) Broadcast(ctx context.Context, tx *transaction.Transaction) (string, error) {
	ok, fail := a.arc.BroadcastCtx(ctx, tx)
	if fail == nil {
		return ok.Txid, nil
	}
	if isFinalFailure(fail.Code) {
		return "", fmt.Errorf("%w: %s", ErrRejected, fail.Description)
	}
	// Transport errors and 5xx. Arc reports a transport error as code "500".
	return "", fmt.Errorf("broadcast failed (%s): %s", fail.Code, fail.Description)
}

// isFinalFailure is true for the HTTP codes that are a verdict on the
// transaction (400, 409, 422 and ARC's 460-469 transaction errors) and for the
// txStatus values ARC answers with HTTP 200 for a transaction the network
// refused. It is false for every other code, which is ours or transient: 401
// and 403 (our API key), 404, 408, 425 and 429 (quota or timing), 5xx,
// transport errors, and a response we cannot read (ERR_INVALID_RESPONSE,
// ERR_TXID_MISMATCH). Calling those a rejection would tell a sender with a
// good transaction that it is dead.
func isFinalFailure(code string) bool {
	if n, err := strconv.Atoi(code); err == nil {
		switch {
		case n == 400, n == 409, n == 422, n >= 460 && n <= 469:
			return true
		case n == 401, n == 403:
			// Our configuration, and an operator has to see it.
			logger.Error("p2p broadcaster refused our credentials", "status", n)
		}
		return false
	}
	switch code {
	case "REJECTED", "DOUBLE_SPEND_ATTEMPTED", "INVALID", "MALFORMED", "MINED_IN_STALE_BLOCK":
		return true
	}
	return false
}
