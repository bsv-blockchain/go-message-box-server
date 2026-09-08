package handlers

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage/storagetest"
)

// fakeStore is an in-memory storage.Store for handler tests. It runs the
// conformance suite itself (TestConformance_Fake) so it cannot drift from the
// contract the real backends implement.
type fakeStore struct {
	mu       sync.Mutex
	clock    time.Time
	messages map[string]*fakeMessage
	perms    map[permKey]*storage.Permission
	devices  map[string]*storage.Device
	fees     map[string]int
}

type fakeMessage struct {
	storage.NewMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

type permKey struct {
	recipient  string
	sender     string
	boxWide    bool
	messageBox string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		clock:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		messages: map[string]*fakeMessage{},
		perms:    map[permKey]*storage.Permission{},
		devices:  map[string]*storage.Device{},
		fees:     map[string]int{},
	}
}

// tick returns a strictly increasing timestamp, so ordering assertions are
// deterministic without sleeping.
func (f *fakeStore) tick() time.Time {
	f.clock = f.clock.Add(time.Millisecond)
	return f.clock
}

func (f *fakeStore) EnsureSchema(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for box, fee := range map[string]int{"notifications": 10, "inbox": 0, "payment_inbox": 0} {
		if _, ok := f.fees[box]; !ok {
			f.fees[box] = fee
		}
	}
	return nil
}

func (f *fakeStore) Close() error { return nil }

func (f *fakeStore) InsertMessage(_ context.Context, m storage.NewMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.messages[m.MessageID]; exists {
		return storage.ErrDuplicateMessage
	}
	ts := f.tick()
	f.messages[m.MessageID] = &fakeMessage{NewMessage: m, CreatedAt: ts, UpdatedAt: ts}
	return nil
}

func (f *fakeStore) ListMessages(_ context.Context, recipient, messageBox string) ([]storage.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []storage.Message
	for _, m := range f.messages {
		if m.Recipient != recipient || m.MessageBox != messageBox {
			continue
		}
		out = append(out, storage.Message{
			MessageID: m.MessageID,
			Sender:    m.Sender,
			Body:      m.Body,
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].MessageID < out[j].MessageID
	})
	return out, nil
}

func (f *fakeStore) AcknowledgeMessages(_ context.Context, recipient string, messageIDs []string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var deleted int64
	for _, id := range messageIDs {
		if m, ok := f.messages[id]; ok && m.Recipient == recipient {
			delete(f.messages, id)
			deleted++
		}
	}
	return deleted, nil
}

func (f *fakeStore) GetServerDeliveryFee(_ context.Context, messageBox string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fees[messageBox], nil
}

func makePermKey(recipient string, sender *string, messageBox string) permKey {
	k := permKey{recipient: recipient, messageBox: messageBox, boxWide: sender == nil}
	if sender != nil {
		k.sender = *sender
	}
	return k
}

func (f *fakeStore) SetPermission(_ context.Context, recipient string, sender *string, messageBox string, recipientFee int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	k := makePermKey(recipient, sender, messageBox)
	ts := f.tick()
	if existing, ok := f.perms[k]; ok {
		existing.RecipientFee = recipientFee
		existing.UpdatedAt = ts
		return nil
	}

	var senderCopy *string
	if sender != nil {
		v := *sender
		senderCopy = &v
	}
	f.perms[k] = &storage.Permission{
		Recipient:    recipient,
		Sender:       senderCopy,
		MessageBox:   messageBox,
		RecipientFee: recipientFee,
		CreatedAt:    ts,
		UpdatedAt:    ts,
	}
	return nil
}

func (f *fakeStore) GetPermission(_ context.Context, recipient string, sender *string, messageBox string) (*storage.Permission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	p, ok := f.perms[makePermKey(recipient, sender, messageBox)]
	if !ok {
		return nil, nil
	}
	clone := *p
	return &clone, nil
}

func (f *fakeStore) ListPermissions(_ context.Context, q storage.PermissionQuery) (storage.PermissionPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var all []storage.Permission
	for _, p := range f.perms {
		if p.Recipient != q.Recipient {
			continue
		}
		if q.MessageBox != nil && p.MessageBox != *q.MessageBox {
			continue
		}
		all = append(all, *p)
	}

	// MessageBox asc, box-wide first, Sender asc, then CreatedAt per q.Order.
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.MessageBox != b.MessageBox {
			return a.MessageBox < b.MessageBox
		}
		if (a.Sender == nil) != (b.Sender == nil) {
			return a.Sender == nil
		}
		if a.Sender != nil && *a.Sender != *b.Sender {
			return *a.Sender < *b.Sender
		}
		if q.Order == storage.SortAsc {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return b.CreatedAt.Before(a.CreatedAt)
	})

	page := storage.PermissionPage{Total: len(all)}
	if q.Offset < len(all) {
		end := min(q.Offset+q.Limit, len(all))
		page.Items = all[q.Offset:end]
	}
	return page, nil
}

func (f *fakeStore) RegisterDevice(_ context.Context, d storage.NewDevice) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	ts := f.tick()
	existing, ok := f.devices[d.FCMToken]
	if !ok {
		existing = &storage.Device{FCMToken: d.FCMToken, CreatedAt: ts}
		f.devices[d.FCMToken] = existing
	}
	existing.IdentityKey = d.IdentityKey
	existing.DeviceID = d.DeviceID
	existing.Platform = d.Platform
	existing.Active = true
	existing.UpdatedAt = ts
	existing.LastUsed = &ts
	return nil
}

func (f *fakeStore) listDevices(identityKey string, activeOnly bool) []storage.Device {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []storage.Device
	for _, d := range f.devices {
		if d.IdentityKey != identityKey || (activeOnly && !d.Active) {
			continue
		}
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[j].UpdatedAt.Before(out[i].UpdatedAt) })
	return out
}

func (f *fakeStore) ListDevices(_ context.Context, identityKey string) ([]storage.Device, error) {
	return f.listDevices(identityKey, false), nil
}

func (f *fakeStore) ListActiveDevices(_ context.Context, identityKey string) ([]storage.Device, error) {
	return f.listDevices(identityKey, true), nil
}

func (f *fakeStore) UpdateDeviceLastUsed(_ context.Context, fcmToken string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if d, ok := f.devices[fcmToken]; ok {
		ts := f.tick()
		d.LastUsed = &ts
		d.UpdatedAt = ts
	}
	return nil
}

func (f *fakeStore) DeactivateDevice(_ context.Context, fcmToken string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if d, ok := f.devices[fcmToken]; ok {
		d.Active = false
		d.UpdatedAt = f.tick()
	}
	return nil
}

var _ storage.Store = (*fakeStore)(nil)

func TestConformance_Fake(t *testing.T) {
	storagetest.RunStoreTests(t, func(t *testing.T) storage.Store {
		s := newFakeStore()
		if err := s.EnsureSchema(context.Background()); err != nil {
			t.Fatal(err)
		}
		return s
	})
}
