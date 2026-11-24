package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/bsv-blockchain/go-message-box-server/internal/firebase"
	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/p2p"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// p2pError carries the paymail error code for a settle failure.
type p2pError struct {
	code, msg string
}

func (e *p2pError) Error() string { return e.msg }

func newP2PError(code, msg string) *p2pError { return &p2pError{code: code, msg: msg} }

// findPayingTx picks the transaction in a BEEF that pays the destination: the
// one with an output to its script for exactly its amount. It is found by
// output, never by position, because senders order BEEFs differently. The
// returned transaction has its ancestors attached for SPV and AtomicBEEF.
func findPayingTx(beef *transaction.Beef, dest *storage.P2PDestination) (*transaction.Transaction, *p2pError) {
	var found *transaction.Transaction
	n := 0
	candidates := 0
	for id, btx := range beef.Transactions {
		if btx.Transaction == nil {
			continue // txid-only entry
		}
		candidates++
		if matchOutput(btx.Transaction, dest) < 0 {
			continue
		}
		id := id
		found = beef.FindAtomicTransactionByHash(&id)
		n++
	}
	switch {
	case candidates == 0:
		return nil, newP2PError("ERR_INVALID_BEEF", "beef contains no transaction")
	case n == 0:
		return nil, newP2PError("ERR_NO_MATCHING_OUTPUT", "no output pays the destination script and amount")
	case n > 1:
		return nil, newP2PError("ERR_NO_MATCHING_OUTPUT", "more than one transaction pays the destination")
	}
	return found, nil
}

// matchOutput is the index of the output paying the destination, or -1.
func matchOutput(tx *transaction.Transaction, dest *storage.P2PDestination) int {
	for i, o := range tx.Outputs {
		if o.LockingScript != nil && o.Satoshis == dest.Satoshis && hex.EncodeToString(o.LockingScript.Bytes()) == dest.Script {
			return i
		}
	}
	return -1
}

// settleP2P takes a payment for a destination from the check to the inbox. The
// order is the safety property: nothing is marked used or delivered until the
// network has accepted the transaction, and a replay of the same transaction
// reaches the same answer without a second message. The status is the HTTP
// status to answer with when err is not nil; err is then a *p2pError.
func (s *Server) settleP2P(ctx context.Context, dest *storage.P2PDestination, tx *transaction.Transaction, meta p2pMetadata) (string, int, error) {
	vout := matchOutput(tx, dest)
	if vout < 0 {
		return "", http.StatusBadRequest, newP2PError("ERR_NO_MATCHING_OUTPUT", "no output pays the destination script and amount")
	}
	txid := tx.TxID().String()
	if dest.UsedTxID != "" && dest.UsedTxID != txid {
		return "", http.StatusConflict, newP2PError("ERR_REFERENCE_USED", "reference already paid by another transaction")
	}

	// A destination already marked with this txid was verified and broadcast
	// by an earlier call, because the mark follows the broadcast. A retry
	// then only has to finish delivery, so a chain tracker outage cannot fail it.
	if dest.UsedTxID != txid {
		if err := s.p2p.Verifier.Verify(ctx, tx); err != nil {
			if errors.Is(err, p2p.ErrInvalidProof) {
				return "", http.StatusBadRequest, newP2PError("ERR_INVALID_PROOF", "transaction or its parents failed SPV verification")
			}
			logger.Error("p2p verify unavailable", "error", err)
			return "", http.StatusServiceUnavailable, newP2PError("ERR_VERIFY_UNAVAILABLE", "could not verify the transaction; safe to retry")
		}
		if _, err := s.p2p.Broadcaster.Broadcast(ctx, tx); err != nil {
			if errors.Is(err, p2p.ErrRejected) {
				return "", http.StatusBadRequest, newP2PError("ERR_REJECTED", err.Error())
			}
			logger.Error("p2p broadcast unavailable", "error", err)
			return "", http.StatusServiceUnavailable, newP2PError("ERR_BROADCAST_UNAVAILABLE", "could not broadcast the transaction; safe to retry")
		}
	}

	// The transaction is on the network and pays the recipient, so nothing
	// below may be cut short by the sender hanging up.
	wctx := context.WithoutCancel(ctx)
	if err := s.destinations.MarkDestinationUsed(wctx, dest.Reference, txid, s.clock()); err != nil {
		// ErrDestinationUsed is a race with another transaction for the same
		// reference. Both pay the recipient, so both are delivered.
		if !errors.Is(err, storage.ErrDestinationUsed) {
			logger.Error("p2p mark used failed", "error", err)
			return "", http.StatusInternalServerError, newP2PError("ERR_INTERNAL", "storage failure; safe to retry")
		}
	}

	atomic, err := tx.AtomicBEEF(false)
	if err != nil {
		logger.Error("p2p atomic beef failed", "error", err)
		return "", http.StatusInternalServerError, newP2PError("ERR_INTERNAL", "could not package the payment; safe to retry")
	}
	tok := p2p.Token{Amount: dest.Satoshis, OutputIndex: vout, Note: meta.Note, Paymail: meta.Sender}
	tok.CustomInstructions.DerivationPrefix = dest.DerivationPrefix
	tok.CustomInstructions.DerivationSuffix = dest.DerivationSuffix
	tok.Transaction = make([]int, len(atomic))
	for i, b := range atomic {
		tok.Transaction[i] = int(b)
	}
	body, err := p2p.InboxBody(tok)
	if err != nil {
		logger.Error("p2p token encode failed", "error", err)
		return "", http.StatusInternalServerError, newP2PError("ERR_INTERNAL", "could not package the payment; safe to retry")
	}

	// The destination's stored key, not the handle's current owner.
	msgID := "p2p-" + txid
	err = s.Store.InsertMessage(wctx, storage.NewMessage{
		MessageID: msgID, Recipient: dest.IdentityKey, MessageBox: paymentInboxBox,
		Sender: p2p.AnyonePublicKeyHex(), Body: body,
	})
	if errors.Is(err, storage.ErrDuplicateMessage) {
		return txid, http.StatusOK, nil // a replay: already delivered, already announced
	}
	if err != nil {
		logger.Error("p2p insert message failed", "error", err)
		return "", http.StatusInternalServerError, newP2PError("ERR_INTERNAL", "storage failure; safe to retry")
	}

	s.pushPayment(wctx, dest.IdentityKey, msgID)
	if raw, err := json.Marshal(body); err == nil {
		s.announceStored(wctx, p2p.AnyonePublicKeyHex(), paymentInboxBox, raw, false,
			[]SendMessageResult{{Recipient: dest.IdentityKey, MessageID: msgID}})
	}
	return txid, http.StatusOK, nil
}

// pushPayment sends the "Payment received" push, as sendMessage does.
func (s *Server) pushPayment(ctx context.Context, recipient, messageID string) {
	if s.push != nil {
		s.push(ctx, recipient, messageID, paymentInboxBox)
		return
	}
	title, _ := pushTitle(paymentInboxBox)
	go firebase.SendFCMNotification(ctx, s.Store, recipient, firebase.FCMPayload{
		Title: title, MessageID: messageID, Recipient: recipient, MessageBox: paymentInboxBox,
	})
}
