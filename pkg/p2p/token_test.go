package p2p

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInboxBodyShape(t *testing.T) {
	var tok Token
	tok.CustomInstructions.DerivationPrefix = "p"
	tok.CustomInstructions.DerivationSuffix = "s"
	tok.Transaction = []int{0, 1, 255}
	tok.Amount = 1000
	tok.OutputIndex = 1

	body, err := InboxBody(tok)
	require.NoError(t, err)

	var outer struct {
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &outer))
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(outer.Message), &m))

	ci := m["customInstructions"].(map[string]any)
	require.Equal(t, "p", ci["derivationPrefix"])
	require.Equal(t, "s", ci["derivationSuffix"])
	txs := m["transaction"].([]any)
	require.Len(t, txs, 3)
	for _, n := range txs {
		f, ok := n.(float64)
		require.True(t, ok)
		require.True(t, f >= 0 && f <= 255)
	}
	require.EqualValues(t, 1000, m["amount"])
	require.EqualValues(t, 1, m["outputIndex"])
	require.NotContains(t, m, "note")
	require.NotContains(t, m, "paymail")
	require.NotContains(t, m, "sender")

	tok.Note = "hi"
	tok.Paymail = "a@b.c"
	body, err = InboxBody(tok)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(body), &outer))
	require.NoError(t, json.Unmarshal([]byte(outer.Message), &m))
	require.Equal(t, "hi", m["note"])
	require.Equal(t, "a@b.c", m["paymail"])
}
