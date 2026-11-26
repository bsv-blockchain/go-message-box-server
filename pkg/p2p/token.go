package p2p

import "encoding/json"

// Token is the payment a PeerPay-style wallet reads from payment_inbox. It
// must never carry a "sender" field: the client drops a message whose token
// sender differs from the message sender.
type Token struct {
	CustomInstructions struct {
		DerivationPrefix string `json:"derivationPrefix"`
		DerivationSuffix string `json:"derivationSuffix"`
	} `json:"customInstructions"`
	Transaction []int  `json:"transaction"` // bytes as JSON numbers, as PeerPay sends them
	Amount      uint64 `json:"amount"`
	OutputIndex int    `json:"outputIndex"`
	Note        string `json:"note,omitempty"`
	Paymail     string `json:"paymail,omitempty"`
}

// InboxBody wraps the token the way the message box stores PeerPay bodies:
// {"message": "<token json as a string>"}.
func InboxBody(t Token) (string, error) {
	_, body, err := InboxMessage(t)
	return body, err
}

// InboxMessage returns both forms of a token message: the token JSON alone,
// which is what a live socket push must carry, and the stored body that wraps
// it. The wrapper exists only in storage; message-box-client unwraps it when it
// lists messages but not when it receives one live.
func InboxMessage(t Token) (token, stored string, err error) {
	inner, err := json.Marshal(t)
	if err != nil {
		return "", "", err
	}
	out, err := json.Marshal(struct {
		Message string `json:"message"`
	}{string(inner)})
	if err != nil {
		return "", "", err
	}
	return string(inner), string(out), nil
}
