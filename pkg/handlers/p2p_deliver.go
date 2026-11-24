package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/bsv-blockchain/go-message-box-server/internal/firebase"
	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/p2p"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// p2pMessageIDPrefix marks inbox messages the paymail rail stored. sendMessage
// refuses client ids with it, so no user can take an id a payment will need.
const p2pMessageIDPrefix = "p2p-"

// p2pMessageID is the inbox id of one payment to one destination. The message
// id is the store's global key, so it names the destination as well as the
// transaction: one transaction may pay several destinations, and each needs its
// own message. Replays of the same pair still collide, as intended.
func p2pMessageID(reference, txid string) string {
	return p2pMessageIDPrefix + reference + "-" + txid
}

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
	// Delivery is recorded on the destination, not inferred from the inbox row:
	// the wallet deletes the row once it has internalized the payment, and a
	// replay after that must not store and announce it again.
	if slices.Contains(dest.DeliveredTxIDs, txid) {
		return txid, http.StatusOK, nil
	}
	// A destination already marked with this txid was verified and broadcast
	// by an earlier call, because the mark follows the broadcast. A retry
	// then only has to finish delivery, so a chain tracker outage cannot fail it.
	//
	// Another txid on a marked destination is not refused. Two transactions can
	// race for one reference and both broadcast; the loser's mark fails by
	// design, so refusing it on retry would strand a payment that is already on
	// the network. Any transaction that pays the destination's script and amount
	// pays the recipient, so it is verified, broadcast (idempotent) and
	// delivered under its own message id.
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
		// ErrDestinationUsed is another transaction holding the mark. Both pay
		// the recipient, so both are delivered.
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
	msgID := p2pMessageID(dest.Reference, txid)
	err = s.Store.InsertMessage(wctx, storage.NewMessage{
		MessageID: msgID, Recipient: dest.IdentityKey, MessageBox: paymentInboxBox,
		Sender: p2p.AnyonePublicKeyHex(), Body: body,
	})
	if errors.Is(err, storage.ErrDuplicateMessage) {
		// The id names this destination and transaction, so a duplicate is a
		// replay only if the stored row is ours. Anything else is a conflict we
		// must not answer 200 to: the payment would never reach the recipient.
		existing, ferr := s.fetchMessagePageRows(wctx, dest.IdentityKey, paymentInboxBox, 0, 1, &msgID)
		if ferr != nil {
			logger.Error("p2p duplicate check failed", "error", ferr)
			return "", http.StatusInternalServerError, newP2PError("ERR_INTERNAL", "storage failure; safe to retry")
		}
		if len(existing) != 1 || existing[0].Sender != p2p.AnyonePublicKeyHex() {
			logger.Error("p2p message id taken by another message")
			return "", http.StatusInternalServerError, newP2PError("ERR_INTERNAL", "could not store the payment message")
		}
		// A replay: the message was stored and announced by an earlier call
		// that failed before it could record delivery.
		return s.recordDelivered(wctx, dest.Reference, txid)
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
	return s.recordDelivered(wctx, dest.Reference, txid)
}

// recordDelivered marks the transaction delivered on its destination. A failure
// is a 5xx: the retry finds the stored message, so it only repeats this write.
func (s *Server) recordDelivered(ctx context.Context, reference, txid string) (string, int, error) {
	if err := s.destinations.MarkDestinationDelivered(ctx, reference, txid); err != nil {
		logger.Error("p2p mark delivered failed", "error", err)
		return "", http.StatusInternalServerError, newP2PError("ERR_INTERNAL", "storage failure; safe to retry")
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
