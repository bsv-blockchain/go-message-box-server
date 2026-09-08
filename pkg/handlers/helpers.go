package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/bsv-blockchain/go-bsv-middleware/pkg/middleware"
	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
)

// feeRow holds fee information for a recipient (used by buildPerRecipientOutputs).
type feeRow struct {
	recipient    string
	recipientFee int
	allowed      bool
}

// OutputMappingError represents an error during output-to-recipient mapping.
type OutputMappingError struct {
	Code        string
	Description string
}

// Error returns error description from OutputMappingError.
func (e *OutputMappingError) Error() string {
	return e.Description
}

// Server holds shared dependencies for all handlers.
type Server struct {
	Store  storage.Store
	wallet sdk.Interface
}

// NewServer creates instance of Server used by all handlers.
func NewServer(store storage.Store, wallet sdk.Interface) *Server {
	return &Server{
		Store:  store,
		wallet: wallet,
	}
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Error("failed to write JSON response", "error", err)
	}
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, ErrorResponse{
		Status:      "error",
		Code:        code,
		Description: description,
	})
}

// getIdentityKey extracts the authenticated identity key from the request context.
// Returns empty string if not authenticated.
func getIdentityKey(r *http.Request) string {
	identity, err := middleware.ShouldGetAuthenticatedIdentity(r.Context())
	if err != nil {
		return ""
	}
	return identity.ToDERHex()
}

// isValidPubKey validates a public key hex string.
func isValidPubKey(key string) bool {
	_, err := ec.PublicKeyFromString(key)
	return err == nil
}

// toSDKInternalizeOutput converts a PaymentOutput to sdk.InternalizeOutput.
func toSDKInternalizeOutput(po PaymentOutput) (sdk.InternalizeOutput, error) {
	out := sdk.InternalizeOutput{
		OutputIndex: po.OutputIndex,
		Protocol:    sdk.InternalizeProtocol(po.Protocol),
	}

	if po.PaymentRemittance != nil {
		prefix, err := base64.StdEncoding.DecodeString(po.PaymentRemittance.DerivationPrefix)
		if err != nil {
			return out, fmt.Errorf("invalid derivationPrefix: %w", err)
		}

		suffix, err := base64.StdEncoding.DecodeString(po.PaymentRemittance.DerivationSuffix)
		if err != nil {
			return out, fmt.Errorf("invalid derivationSuffix: %w", err)
		}

		senderKey, err := ec.PublicKeyFromString(po.PaymentRemittance.SenderIdentityKey)
		if err != nil {
			return out, fmt.Errorf("invalid senderIdentityKey: %w", err)
		}

		out.PaymentRemittance = &sdk.Payment{
			DerivationPrefix:  prefix,
			DerivationSuffix:  suffix,
			SenderIdentityKey: senderKey,
		}
	}

	if po.InsertionRemittance != nil {
		out.InsertionRemittance = &sdk.BasketInsertion{
			Basket:             po.InsertionRemittance.Basket,
			CustomInstructions: string(po.InsertionRemittance.CustomInstructions),
			Tags:               po.InsertionRemittance.Tags,
		}
	}

	return out, nil
}

// buildPerRecipientOutputs maps payment outputs to recipients based on customInstructions or positional fallback.
func buildPerRecipientOutputs(outputs []PaymentOutput, deliveryFee int, feeRows []feeRow) (map[string][]PaymentOutput, error) {
	perRecipientOutputs := make(map[string][]PaymentOutput)
	// skip first output if it was for server delivery fee
	startIdx := 0
	if deliveryFee > 0 && len(outputs) > 0 {
		startIdx = 1
	}

	recipientSideOutputs := outputs[startIdx:]

	// Get recipients that require payment
	var feeRecipients []string
	for _, fr := range feeRows {
		if fr.recipientFee > 0 {
			feeRecipients = append(feeRecipients, fr.recipient)
		}
	}

	if len(feeRecipients) == 0 {
		return perRecipientOutputs, nil
	}

	// try explicit mapping via customInstructions.recipientIdentityKey
	outputsByRecipientKey := make(map[string][]PaymentOutput)
	usedIndexes := make(map[uint32]bool)

	for _, out := range recipientSideOutputs {
		recipientKey := extractRecipientKey(out)
		if recipientKey != "" {
			outputsByRecipientKey[recipientKey] = append(outputsByRecipientKey[recipientKey], out)
			usedIndexes[out.OutputIndex] = true
		}
	}

	if len(outputsByRecipientKey) == 0 {
		if len(recipientSideOutputs) < len(feeRecipients) {
			return nil, &OutputMappingError{
				Code:        "ERR_INSUFFICIENT_OUTPUTS",
				Description: fmt.Sprintf("Expected at least %d recipient output(s) but received %d", len(feeRecipients), len(recipientSideOutputs)),
			}
		}
		// positional fallback
		for i, r := range feeRecipients {
			if i < len(recipientSideOutputs) {
				perRecipientOutputs[r] = []PaymentOutput{recipientSideOutputs[i]}
			}
		}
	} else {
		// use tagged outputs
		for _, r := range feeRecipients {
			if tagged, ok := outputsByRecipientKey[r]; ok {
				perRecipientOutputs[r] = tagged
			}
		}

		// for any remaining fee recipients without tags, allocate unused outputs (positional)
		var unmapped []string
		for _, r := range feeRecipients {
			if _, ok := perRecipientOutputs[r]; !ok {
				unmapped = append(unmapped, r)
			}
		}

		if len(unmapped) > 0 {
			// filter to remaining (unused) outputs
			var remaining []PaymentOutput
			for _, out := range recipientSideOutputs {
				if !usedIndexes[out.OutputIndex] {
					remaining = append(remaining, out)
				}
			}

			if len(remaining) < len(unmapped) {
				return nil, &OutputMappingError{
					Code:        "ERR_INSUFFICIENT_OUTPUTS",
					Description: fmt.Sprintf("Expected at least %d additional recipient output(s) but only %d remain", len(unmapped), len(remaining)),
				}
			}

			for i, r := range unmapped {
				perRecipientOutputs[r] = []PaymentOutput{remaining[i]}
			}
		}
	}

	return perRecipientOutputs, nil
}

// extractRecipientKey extracts recipientIdentityKey from customInstructions.
func extractRecipientKey(out PaymentOutput) string {
	var raw json.RawMessage
	var instr struct {
		RecipientIdentityKey string `json:"recipientIdentityKey"`
	}

	if out.PaymentRemittance != nil && len(out.PaymentRemittance.CustomInstructions) > 0 {
		raw = out.PaymentRemittance.CustomInstructions
	} else if out.InsertionRemittance != nil && len(out.InsertionRemittance.CustomInstructions) > 0 {
		raw = out.InsertionRemittance.CustomInstructions
	}

	if len(raw) == 0 {
		return ""
	}
	if err := json.Unmarshal(raw, &instr); err != nil {
		return ""
	}
	return instr.RecipientIdentityKey
}
