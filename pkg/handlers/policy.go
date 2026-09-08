package handlers

import (
	"context"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// notificationsBox is the message box that carries push notifications and, by
// default, charges a fee.
const notificationsBox = "notifications"

// smartDefaultFee is the recipient fee assigned to a message box that has no
// permission row yet.
func smartDefaultFee(messageBox string) int {
	if messageBox == notificationsBox {
		return 10
	}
	return 0
}

// shouldUseFCMDelivery reports whether delivery to this message box should also
// trigger a push notification.
func shouldUseFCMDelivery(messageBox string) bool {
	return messageBox == notificationsBox
}

// recipientFee resolves the fee a sender must pay this recipient, trying the
// sender-specific permission, then the box-wide default, then the smart default
// — which it persists, so the row appears in /permissions/list just as it did
// when this lived in the SQL layer.
func (s *Server) recipientFee(ctx context.Context, recipient, sender, messageBox string) (int, error) {
	if sender != "" {
		p, err := s.Store.GetPermission(ctx, recipient, &sender, messageBox)
		if err != nil {
			return 0, err
		}
		if p != nil {
			return p.RecipientFee, nil
		}
	}

	p, err := s.Store.GetPermission(ctx, recipient, nil, messageBox)
	if err != nil {
		return 0, err
	}
	if p != nil {
		return p.RecipientFee, nil
	}

	fee := smartDefaultFee(messageBox)
	if err := s.Store.SetPermission(ctx, recipient, nil, messageBox, fee); err != nil {
		return 0, err
	}
	return fee, nil
}

// sortOrder maps the createdAtOrder query parameter onto the storage enum.
// Anything other than "asc" is descending, matching the previous behaviour.
func sortOrder(param string) storage.SortOrder {
	if param == "asc" {
		return storage.SortAsc
	}
	return storage.SortDesc
}
