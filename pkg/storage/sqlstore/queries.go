package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// --- messages ---------------------------------------------------------------

// ensureMessageBox creates the messageBox row if it doesn't exist and returns
// its id. The id is a SQL implementation detail and never leaves this package.
func (s *Store) ensureMessageBox(ctx context.Context, identityKey, boxType string) (int64, error) {
	now := time.Now()
	_, err := s.exec(ctx,
		`INSERT INTO messageBox (identityKey, type, created_at, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (type, identityKey) DO NOTHING`,
		identityKey, boxType, now, now,
	)
	if err != nil {
		return 0, err
	}

	var id int64
	err = s.queryRow(ctx, `SELECT messageBoxId FROM messageBox WHERE identityKey = ? AND type = ?`, identityKey, boxType).Scan(&id)
	return id, err
}

// messageBoxID returns the messageBoxId for a given identity and type, or 0 if
// the box does not exist.
func (s *Store) messageBoxID(ctx context.Context, identityKey, boxType string) (int64, error) {
	var id int64
	err := s.queryRow(ctx, `SELECT messageBoxId FROM messageBox WHERE identityKey = ? AND type = ?`, identityKey, boxType).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// InsertMessage implements storage.MessageStore.
func (s *Store) InsertMessage(ctx context.Context, m storage.NewMessage) error {
	boxID, err := s.ensureMessageBox(ctx, m.Recipient, m.MessageBox)
	if err != nil {
		return err
	}

	now := time.Now()
	res, err := s.exec(ctx,
		`INSERT INTO messages (messageId, messageBoxId, sender, recipient, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (messageId) DO NOTHING`,
		m.MessageID, boxID, m.Sender, m.Recipient, m.Body, now, now,
	)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return storage.ErrDuplicateMessage
	}
	return nil
}

// ListMessages implements storage.MessageStore.
func (s *Store) ListMessages(ctx context.Context, recipient, messageBox string) ([]storage.Message, error) {
	boxID, err := s.messageBoxID(ctx, recipient, messageBox)
	if err != nil {
		return nil, err
	}
	if boxID == 0 {
		return nil, nil
	}

	rows, err := s.query(ctx,
		`SELECT messageId, body, sender, created_at, updated_at FROM messages
		 WHERE recipient = ? AND messageBoxId = ?
		 ORDER BY created_at ASC, messageId ASC`,
		recipient, boxID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []storage.Message
	for rows.Next() {
		var m storage.Message
		if err := rows.Scan(&m.MessageID, &m.Body, &m.Sender, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// AcknowledgeMessages implements storage.MessageStore.
func (s *Store) AcknowledgeMessages(ctx context.Context, recipient string, messageIDs []string) (int64, error) {
	if len(messageIDs) == 0 {
		return 0, nil
	}
	// Build placeholders
	query := `DELETE FROM messages WHERE recipient = ? AND messageId IN (`
	args := []any{recipient}
	for i, id := range messageIDs {
		if i > 0 {
			query += ","
		}
		query += "?"
		args = append(args, id)
	}
	query += ")"
	res, err := s.exec(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- fees -------------------------------------------------------------------

// GetServerDeliveryFee implements storage.FeeStore.
func (s *Store) GetServerDeliveryFee(ctx context.Context, messageBox string) (int, error) {
	var fee int
	err := s.queryRow(ctx, `SELECT delivery_fee FROM server_fees WHERE message_box = ?`, messageBox).Scan(&fee)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return fee, err
}

// --- permissions ------------------------------------------------------------

const permissionColumns = `id, recipient, sender, message_box, recipient_fee, created_at, updated_at`

// scanPermission reads one permission row. The id column is scanned and
// discarded: it is a SQL surrogate key that the contract does not expose.
func scanPermission(sc interface{ Scan(...any) error }) (storage.Permission, error) {
	var (
		p      storage.Permission
		id     int64
		sender sql.NullString
	)
	err := sc.Scan(&id, &p.Recipient, &sender, &p.MessageBox, &p.RecipientFee, &p.CreatedAt, &p.UpdatedAt)
	p.Sender = nullStr(sender)
	return p, err
}

// SetPermission implements storage.PermissionStore.
func (s *Store) SetPermission(ctx context.Context, recipient string, sender *string, messageBox string, recipientFee int) error {
	now := time.Now()

	// NULL != NULL in unique constraints for both SQLite and PostgreSQL, so we need special handling
	if sender == nil {
		// Try update first
		res, err := s.exec(ctx,
			`UPDATE message_permissions SET recipient_fee = ?, updated_at = ? WHERE recipient = ? AND sender IS NULL AND message_box = ?`,
			recipientFee, now, recipient, messageBox,
		)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected > 0 {
			return nil
		}
		// Insert
		_, err = s.exec(ctx,
			`INSERT INTO message_permissions (recipient, sender, message_box, recipient_fee, created_at, updated_at) VALUES (?, NULL, ?, ?, ?, ?)`,
			recipient, messageBox, recipientFee, now, now,
		)
		return err
	}

	// For non-null sender, ON CONFLICT works fine
	_, err := s.exec(ctx,
		`INSERT INTO message_permissions (recipient, sender, message_box, recipient_fee, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(recipient, sender, message_box) DO UPDATE SET recipient_fee = ?, updated_at = ?`,
		recipient, *sender, messageBox, recipientFee, now, now, recipientFee, now,
	)
	return err
}

// SetPermissionIfAbsent implements storage.PermissionStore.
//
// UNIQUE(recipient, sender, message_box) does not constrain rows with a NULL
// sender, since NULL != NULL, so the box-wide case cannot rely on ON CONFLICT
// and guards with a NOT EXISTS subquery instead. That single statement is
// atomic under SQLite, whose writers are serialised, but not under PostgreSQL's
// READ COMMITTED: two concurrent callers can both pass the NOT EXISTS and both
// insert. Neither one modifies an existing row, so the outcome is a duplicate
// box-wide permission rather than a lost one.
//
// Closing the gap needs an expression unique index over
// (recipient, COALESCE(sender, ”), message_box), which cannot be created on a
// database that already holds duplicates, so it wants a dedupe migration of
// its own.
func (s *Store) SetPermissionIfAbsent(ctx context.Context, recipient string, sender *string, messageBox string, recipientFee int) error {
	now := time.Now()

	if sender == nil {
		_, err := s.exec(ctx,
			`INSERT INTO message_permissions (recipient, sender, message_box, recipient_fee, created_at, updated_at)
			 SELECT ?, NULL, ?, ?, ?, ?
			 WHERE NOT EXISTS (
			   SELECT 1 FROM message_permissions WHERE recipient = ? AND sender IS NULL AND message_box = ?
			 )`,
			recipient, messageBox, recipientFee, now, now,
			recipient, messageBox,
		)
		return err
	}

	_, err := s.exec(ctx,
		`INSERT INTO message_permissions (recipient, sender, message_box, recipient_fee, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(recipient, sender, message_box) DO NOTHING`,
		recipient, *sender, messageBox, recipientFee, now, now,
	)
	return err
}

// GetPermission implements storage.PermissionStore.
func (s *Store) GetPermission(ctx context.Context, recipient string, sender *string, messageBox string) (*storage.Permission, error) {
	var row *sql.Row
	if sender != nil {
		row = s.queryRow(ctx,
			`SELECT `+permissionColumns+` FROM message_permissions WHERE recipient = ? AND sender = ? AND message_box = ?`,
			recipient, *sender, messageBox,
		)
	} else {
		row = s.queryRow(ctx,
			`SELECT `+permissionColumns+` FROM message_permissions WHERE recipient = ? AND sender IS NULL AND message_box = ?`,
			recipient, messageBox,
		)
	}

	p, err := scanPermission(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPermissions implements storage.PermissionStore.
func (s *Store) ListPermissions(ctx context.Context, q storage.PermissionQuery) (storage.PermissionPage, error) {
	var page storage.PermissionPage

	where := ` WHERE recipient = ?`
	args := []any{q.Recipient}
	if q.MessageBox != nil {
		where += ` AND message_box = ?`
		args = append(args, *q.MessageBox)
	}

	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM message_permissions`+where, args...).Scan(&page.Total); err != nil {
		return storage.PermissionPage{}, err
	}

	// The order is fixed by the contract; only the created_at direction varies,
	// and it comes from a closed set of literals, never from the caller's string.
	direction := "DESC"
	if q.Order == storage.SortAsc {
		direction = "ASC"
	}
	query := `SELECT ` + permissionColumns + ` FROM message_permissions` + where +
		` ORDER BY message_box ASC, CASE WHEN sender IS NULL THEN 0 ELSE 1 END, sender ASC, created_at ` + direction +
		` LIMIT ? OFFSET ?`
	args = append(args, q.Limit, q.Offset)

	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return storage.PermissionPage{}, err
	}
	defer rows.Close()

	for rows.Next() {
		p, err := scanPermission(rows)
		if err != nil {
			return storage.PermissionPage{}, err
		}
		page.Items = append(page.Items, p)
	}
	if err := rows.Err(); err != nil {
		return storage.PermissionPage{}, err
	}
	return page, nil
}

// --- devices ----------------------------------------------------------------

const deviceColumns = `identity_key, fcm_token, device_id, platform, active, created_at, updated_at, last_used`

// RegisterDevice implements storage.DeviceStore.
func (s *Store) RegisterDevice(ctx context.Context, d storage.NewDevice) error {
	now := time.Now()
	_, err := s.exec(ctx,
		`INSERT INTO device_registrations (identity_key, fcm_token, device_id, platform, created_at, updated_at, active, last_used)
		 VALUES (?, ?, ?, ?, ?, ?, TRUE, ?)
		 ON CONFLICT(fcm_token) DO UPDATE SET identity_key = ?, device_id = ?, platform = ?, updated_at = ?, active = TRUE, last_used = ?`,
		d.IdentityKey, d.FCMToken, d.DeviceID, d.Platform, now, now, now,
		d.IdentityKey, d.DeviceID, d.Platform, now, now,
	)
	return err
}

// listDevices runs the shared device query with an optional active filter.
func (s *Store) listDevices(ctx context.Context, identityKey string, activeOnly bool) ([]storage.Device, error) {
	query := `SELECT ` + deviceColumns + ` FROM device_registrations WHERE identity_key = ?`
	if activeOnly {
		query += ` AND active = TRUE`
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := s.query(ctx, query, identityKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []storage.Device
	for rows.Next() {
		var (
			d        storage.Device
			deviceID sql.NullString
			platform sql.NullString
			lastUsed sql.NullTime
		)
		if err := rows.Scan(&d.IdentityKey, &d.FCMToken, &deviceID, &platform, &d.Active, &d.CreatedAt, &d.UpdatedAt, &lastUsed); err != nil {
			return nil, err
		}
		d.DeviceID = nullStr(deviceID)
		d.Platform = nullStr(platform)
		d.LastUsed = nullTime(lastUsed)
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

// ListDevices implements storage.DeviceStore.
func (s *Store) ListDevices(ctx context.Context, identityKey string) ([]storage.Device, error) {
	return s.listDevices(ctx, identityKey, false)
}

// ListActiveDevices implements storage.DeviceStore.
func (s *Store) ListActiveDevices(ctx context.Context, identityKey string) ([]storage.Device, error) {
	return s.listDevices(ctx, identityKey, true)
}

// UpdateDeviceLastUsed implements storage.DeviceStore.
func (s *Store) UpdateDeviceLastUsed(ctx context.Context, fcmToken string) error {
	now := time.Now()
	_, err := s.exec(ctx,
		`UPDATE device_registrations SET last_used = ?, updated_at = ? WHERE fcm_token = ?`,
		now, now, fcmToken,
	)
	return err
}

// DeactivateDevice implements storage.DeviceStore.
func (s *Store) DeactivateDevice(ctx context.Context, fcmToken string) error {
	_, err := s.exec(ctx,
		`UPDATE device_registrations SET active = FALSE, updated_at = ? WHERE fcm_token = ?`,
		time.Now(), fcmToken,
	)
	return err
}
