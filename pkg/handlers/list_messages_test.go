package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage/sqlstore"
)

// raw is a small helper so table-driven tests can write JSON fragments as Go
// literals instead of byte slices.
func raw(v string) json.RawMessage {
	if v == "" {
		return nil
	}
	return json.RawMessage(v)
}

func TestDefaultListMessagesConfig(t *testing.T) {
	// These pin the TS reference server's "standard" resource profile
	// (config/resources.ts): a client sending only messageBox must get the same
	// page size an unconfigured TS deployment would.
	cfg := defaultListMessagesConfig()
	if cfg.DefaultLimit != 1000 {
		t.Errorf("DefaultLimit = %d, want 1000", cfg.DefaultLimit)
	}
	if cfg.MaxLimit != 1000 {
		t.Errorf("MaxLimit = %d, want 1000", cfg.MaxLimit)
	}
	if cfg.MaxOffset != 100_000 {
		t.Errorf("MaxOffset = %d, want 100000", cfg.MaxOffset)
	}
	if cfg.MaxResponseBytes != 8*1024*1024 {
		t.Errorf("MaxResponseBytes = %d, want 8MiB", cfg.MaxResponseBytes)
	}
}

func TestNewServer_UsesListMessagesDefaults(t *testing.T) {
	srv := NewServer(newFakeStore(), nil)
	if srv.listMessages != defaultListMessagesConfig() {
		t.Errorf("NewServer's listMessages = %+v, want the defaults", srv.listMessages)
	}
}

func TestSetListMessagesConfig(t *testing.T) {
	srv := NewServer(newFakeStore(), nil)
	custom := ListMessagesConfig{DefaultLimit: 5, MaxLimit: 5, MaxOffset: 10, MaxResponseBytes: 1024}
	srv.SetListMessagesConfig(custom)
	if srv.listMessages != custom {
		t.Errorf("listMessages = %+v, want %+v", srv.listMessages, custom)
	}
}

// TestDecodeListMessagesRequest_EmptyBody pins the fix for treating an empty
// body as a JSON-parse error: json.Decoder reports io.EOF for a zero-length
// body, but the TS reference server's body-parser.json() defaults an empty
// body to `{}` and falls through to ordinary field validation, so an empty
// body here must decode to ListMessagesRequest{}'s zero value (which
// normalizeMessageBoxName then reports as ERR_MESSAGEBOX_REQUIRED, exactly as
// `{}` would in TS) rather than a bespoke ERR_INVALID_JSON with no TS
// counterpart.
func TestDecodeListMessagesRequest_EmptyBody(t *testing.T) {
	req := httptest.NewRequest("POST", "/listMessages", nil)
	got, rf := decodeListMessagesRequest(req)
	if rf != nil {
		t.Fatalf("rf = %+v, want nil for an empty body", rf)
	}
	if len(got.MessageBox) != 0 || len(got.Limit) != 0 || len(got.Offset) != 0 || len(got.Skip) != 0 || len(got.MessageID) != 0 {
		t.Errorf("got = %+v, want every field empty", got)
	}
}

// TestDecodeListMessagesRequest_MalformedBody confirms a genuinely malformed,
// non-empty body is still ERR_INVALID_JSON: only an empty body gets the "{}"
// treatment.
func TestDecodeListMessagesRequest_MalformedBody(t *testing.T) {
	req := httptest.NewRequest("POST", "/listMessages", strings.NewReader(`{not json`))
	_, rf := decodeListMessagesRequest(req)
	if rf == nil || rf.code != "ERR_INVALID_JSON" {
		t.Fatalf("rf = %+v, want ERR_INVALID_JSON", rf)
	}
}

func TestNormalizeMessageBoxName(t *testing.T) {
	longName := ""
	for i := 0; i < maxMessageBoxBytes+1; i++ {
		longName += "x"
	}

	cases := []struct {
		name    string
		raw     json.RawMessage
		want    string
		wantErr string // empty means no error
	}{
		{"missing", raw(""), "", "ERR_MESSAGEBOX_REQUIRED"},
		{"null", raw(`null`), "", "ERR_MESSAGEBOX_REQUIRED"},
		{"empty string", raw(`""`), "", "ERR_MESSAGEBOX_REQUIRED"},
		{"whitespace only", raw(`"   "`), "", "ERR_MESSAGEBOX_REQUIRED"},
		{"number", raw(`123`), "", "ERR_INVALID_MESSAGEBOX"},
		{"bool", raw(`true`), "", "ERR_INVALID_MESSAGEBOX"},
		{"object", raw(`{"a":1}`), "", "ERR_INVALID_MESSAGEBOX"},
		{"array", raw(`["inbox"]`), "", "ERR_INVALID_MESSAGEBOX"},
		{"malformed json", raw(`{not json`), "", "ERR_INVALID_MESSAGEBOX"},
		{"leading space", raw(`" inbox"`), "", "ERR_INVALID_MESSAGEBOX"},
		{"trailing space", raw(`"inbox "`), "", "ERR_INVALID_MESSAGEBOX"},
		{"embedded newline", raw(`"in\nbox"`), "", "ERR_INVALID_MESSAGEBOX"},
		{"embedded NEL", raw(`"in\u0085box"`), "", "ERR_INVALID_MESSAGEBOX"},
		{"oversized", raw(`"` + longName + `"`), "", "ERR_INVALID_MESSAGEBOX"},
		{"valid", raw(`"inbox"`), "inbox", ""},
		{"valid payment_inbox", raw(`"payment_inbox"`), "payment_inbox", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, rf := normalizeMessageBoxName(c.raw)
			if c.wantErr == "" {
				if rf != nil {
					t.Fatalf("got failure %+v, want none", rf)
				}
				if got != c.want {
					t.Errorf("got %q, want %q", got, c.want)
				}
				return
			}
			if rf == nil {
				t.Fatalf("got no failure, want code %s", c.wantErr)
			}
			if rf.code != c.wantErr {
				t.Errorf("code = %s, want %s", rf.code, c.wantErr)
			}
			if rf.status != 400 {
				t.Errorf("status = %d, want 400", rf.status)
			}
		})
	}

	// The "must be a string" message is pinned separately: it is the one TS
	// wording this handler is expected to reproduce verbatim.
	t.Run("non-string message text", func(t *testing.T) {
		_, rf := normalizeMessageBoxName(raw(`123`))
		if rf == nil || rf.desc != "MessageBox name must be a string!" {
			t.Errorf("got %+v, want the exact TS wording", rf)
		}
	})
}

func TestParseOptionalMessageID(t *testing.T) {
	longID := ""
	for i := 0; i < maxMessageIDBytes+1; i++ {
		longID += "x"
	}

	cases := []struct {
		name    string
		raw     json.RawMessage
		want    *string
		wantErr string
	}{
		{"missing", raw(""), nil, ""},
		// Unlike messageBox's normalizeMessageBoxName (which treats missing and
		// null alike via JS's `== null`), TS's messageId check is the strict
		// `messageId !== undefined`: an explicit null is present and fails
		// isCanonicalMessageId, so it is ERR_INVALID_MESSAGE_ID, not "no filter".
		{"null", raw(`null`), nil, "ERR_INVALID_MESSAGE_ID"},
		{"number", raw(`123`), nil, "ERR_INVALID_MESSAGE_ID"},
		{"empty string", raw(`""`), nil, "ERR_INVALID_MESSAGE_ID"},
		{"control char", raw(`"m\nsg"`), nil, "ERR_INVALID_MESSAGE_ID"},
		{"oversized", raw(`"` + longID + `"`), nil, "ERR_INVALID_MESSAGE_ID"},
		{"valid", raw(`"msg-123"`), ptrStr("msg-123"), ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, rf := parseOptionalMessageID(c.raw)
			if c.wantErr == "" {
				if rf != nil {
					t.Fatalf("got failure %+v, want none", rf)
				}
				if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
					t.Errorf("got %v, want %v", strPtrStr(got), strPtrStr(c.want))
				}
				return
			}
			if rf == nil || rf.code != c.wantErr {
				t.Fatalf("got %+v, want code %s", rf, c.wantErr)
			}
		})
	}
}

func ptrStr(s string) *string { return &s }
func strPtrStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestParseListPagination(t *testing.T) {
	cfg := ListMessagesConfig{DefaultLimit: 1000, MaxLimit: 1000, MaxOffset: 100_000, MaxResponseBytes: 8 << 20}

	cases := []struct {
		name       string
		limit      json.RawMessage
		offset     json.RawMessage
		skip       json.RawMessage
		cfg        ListMessagesConfig
		wantLimit  int
		wantOffset int
		wantErr    string
	}{
		{"all defaults", raw(""), raw(""), raw(""), cfg, 1000, 0, ""},
		{"explicit limit and offset", raw(`10`), raw(`5`), raw(""), cfg, 10, 5, ""},
		{"limit over max", raw(fmt.Sprintf("%d", cfg.MaxLimit+1)), raw(""), raw(""), cfg, 0, 0, "ERR_INVALID_LIMIT"},
		{"non-integer limit", raw(`1.5`), raw(""), raw(""), cfg, 0, 0, "ERR_INVALID_LIMIT"},
		{"zero limit", raw(`0`), raw(""), raw(""), cfg, 0, 0, "ERR_INVALID_LIMIT"},
		{"string limit", raw(`"5"`), raw(""), raw(""), cfg, 0, 0, "ERR_INVALID_LIMIT"},
		{"offset over max", raw(""), raw(fmt.Sprintf("%d", cfg.MaxOffset+1)), raw(""), cfg, 0, 0, "ERR_INVALID_OFFSET"},
		{"negative offset", raw(""), raw(`-1`), raw(""), cfg, 0, 0, "ERR_INVALID_OFFSET"},
		{"skip alone used as offset", raw(""), raw(""), raw(`7`), cfg, 1000, 7, ""},
		{"offset and skip match", raw(""), raw(`7`), raw(`7`), cfg, 1000, 7, ""},
		{"offset and skip mismatch", raw(""), raw(`7`), raw(`8`), cfg, 0, 0, "ERR_INVALID_OFFSET"},
		{"offset and skip type mismatch", raw(""), raw(`7`), raw(`"7"`), cfg, 0, 0, "ERR_INVALID_OFFSET"},
		{
			"unlimited max limit accepts large value",
			raw(`50000`), raw(""), raw(""),
			ListMessagesConfig{DefaultLimit: 1000, MaxLimit: -1, MaxOffset: 100_000, MaxResponseBytes: 8 << 20},
			50000, 0, "",
		},
		{
			// Mirrors TS's `resources.listDefaultLimit === -1 ? Number.MAX_SAFE_INTEGER
			// : resources.listDefaultLimit`: an operator who configures both
			// LIST_DEFAULT_LIMIT and LIST_MAX_LIMIT as -1 (both individually legal
			// per pkg/config's getEnvListBound) must still get a successful,
			// unbounded page when limit is omitted, not ERR_INVALID_LIMIT from a
			// literal -1 failing boundedInteger's minimum-1 check.
			"unlimited default limit with unlimited max succeeds",
			raw(""), raw(""), raw(""),
			ListMessagesConfig{DefaultLimit: -1, MaxLimit: -1, MaxOffset: 100_000, MaxResponseBytes: 8 << 20},
			safeIntegerLimit, 0, "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, rf := parseListPagination(c.limit, c.offset, c.skip, c.cfg)
			if c.wantErr == "" {
				if rf != nil {
					t.Fatalf("got failure %+v, want none", rf)
				}
				if got.Limit != c.wantLimit || got.Offset != c.wantOffset {
					t.Errorf("got %+v, want limit=%d offset=%d", got, c.wantLimit, c.wantOffset)
				}
				return
			}
			if rf == nil || rf.code != c.wantErr {
				t.Fatalf("got %+v, want code %s", rf, c.wantErr)
			}
			if rf.status != 400 {
				t.Errorf("status = %d, want 400", rf.status)
			}
		})
	}
}

// TestBoundDisplay pins the exact wording TS's listMessages.ts renders for an
// unlimited bound (`resources.listMaxLimit === -1 ? 'the JavaScript
// safe-integer maximum' : String(resources.listMaxLimit)`), so
// ERR_INVALID_LIMIT/ERR_INVALID_OFFSET's description text is byte-for-byte
// identical to what the TS reference server would send for the same
// configuration.
func TestBoundDisplay(t *testing.T) {
	if got := boundDisplay(-1); got != "the JavaScript safe-integer maximum" {
		t.Errorf("boundDisplay(-1) = %q, want %q", got, "the JavaScript safe-integer maximum")
	}
	if got := boundDisplay(1000); got != "1000" {
		t.Errorf("boundDisplay(1000) = %q, want %q", got, "1000")
	}
}

// --- readMessagePage: end-to-end behaviour against the fallback (non-pager)
// path, since fakeStore deliberately does not implement storage.MessagePager
// (see fakestore_test.go). This is what an external Store that only
// implements the required interfaces gets. ---

func insertMsgs(t *testing.T, srv *Server, recipient, box string, ids []string) {
	t.Helper()
	for _, id := range ids {
		if err := srv.Store.InsertMessage(context.Background(), storage.NewMessage{
			MessageID: id, Recipient: recipient, MessageBox: box, Sender: "02sender", Body: id,
		}); err != nil {
			t.Fatalf("InsertMessage(%s): %v", id, err)
		}
	}
}

func TestReadMessagePage_NonexistentBox(t *testing.T) {
	srv := setupTestServer(t)
	ctx := context.Background()

	resp, rf, err := srv.readMessagePage(ctx, mockIdentityKey, "nosuchbox", listPagination{Limit: 10, Offset: 3}, nil, -1)
	if err != nil || rf != nil {
		t.Fatalf("err=%v rf=%+v, want neither", err, rf)
	}
	if len(resp.Messages) != 0 {
		t.Errorf("Messages = %v, want empty", resp.Messages)
	}
	if resp.Limit != 10 || resp.Offset != 3 || resp.NextOffset != 3 || resp.HasMore {
		t.Errorf("envelope = %+v, want limit=10 offset=3 nextOffset=3 hasMore=false", resp)
	}
}

func TestReadMessagePage_ReturnsMessagesInOrder(t *testing.T) {
	srv := setupTestServer(t)
	insertMsgs(t, srv, mockIdentityKey, "inbox", []string{"m1"})

	resp, rf, err := srv.readMessagePage(context.Background(), mockIdentityKey, "inbox", listPagination{Limit: 1000, Offset: 0}, nil, -1)
	if err != nil || rf != nil {
		t.Fatalf("err=%v rf=%+v", err, rf)
	}
	if len(resp.Messages) != 1 || resp.Messages[0].MessageID != "m1" {
		t.Fatalf("Messages = %+v, want [m1]", resp.Messages)
	}
	if resp.Messages[0].Sender != "02sender" || resp.Messages[0].Body != "m1" {
		t.Errorf("message shape = %+v", resp.Messages[0])
	}
	if resp.NextOffset != 1 || resp.HasMore {
		t.Errorf("envelope = %+v, want nextOffset=1 hasMore=false", resp)
	}
}

// Mirrors the TS test "detects another page when the query batch exactly
// fills the requested limit": with exactly one more message beyond the
// limit, hasMore must be true and nextOffset must equal the limit, not the
// full inbox size.
func TestReadMessagePage_HasMoreWhenExactlyAtLimit(t *testing.T) {
	srv := setupTestServer(t)
	ids := make([]string, 9)
	for i := range ids {
		ids[i] = fmt.Sprintf("msg-%d", i+1)
	}
	insertMsgs(t, srv, mockIdentityKey, "inbox", ids)

	resp, rf, err := srv.readMessagePage(context.Background(), mockIdentityKey, "inbox", listPagination{Limit: 8, Offset: 0}, nil, -1)
	if err != nil || rf != nil {
		t.Fatalf("err=%v rf=%+v", err, rf)
	}
	if len(resp.Messages) != 8 {
		t.Fatalf("got %d messages, want 8", len(resp.Messages))
	}
	if resp.Messages[0].MessageID != "msg-1" || resp.Messages[7].MessageID != "msg-8" {
		t.Errorf("range = [%s .. %s], want [msg-1 .. msg-8]", resp.Messages[0].MessageID, resp.Messages[7].MessageID)
	}
	if resp.Limit != 8 || resp.Offset != 0 || resp.NextOffset != 8 || !resp.HasMore {
		t.Errorf("envelope = %+v, want limit=8 offset=0 nextOffset=8 hasMore=true", resp)
	}
}

func TestReadMessagePage_ExactlyFillsWithNoMoreLeavesHasMoreFalse(t *testing.T) {
	srv := setupTestServer(t)
	insertMsgs(t, srv, mockIdentityKey, "inbox", []string{"m1", "m2"})

	resp, rf, err := srv.readMessagePage(context.Background(), mockIdentityKey, "inbox", listPagination{Limit: 2, Offset: 0}, nil, -1)
	if err != nil || rf != nil {
		t.Fatalf("err=%v rf=%+v", err, rf)
	}
	if len(resp.Messages) != 2 || resp.HasMore {
		t.Errorf("got %d messages hasMore=%v, want 2 messages hasMore=false", len(resp.Messages), resp.HasMore)
	}
}

func TestReadMessagePage_MessageIDFilter(t *testing.T) {
	srv := setupTestServer(t)
	insertMsgs(t, srv, mockIdentityKey, "inbox", []string{"m1", "m2", "m3"})

	t.Run("match", func(t *testing.T) {
		id := "m2"
		resp, rf, err := srv.readMessagePage(context.Background(), mockIdentityKey, "inbox", listPagination{Limit: 1000, Offset: 0}, &id, -1)
		if err != nil || rf != nil {
			t.Fatalf("err=%v rf=%+v", err, rf)
		}
		if len(resp.Messages) != 1 || resp.Messages[0].MessageID != "m2" {
			t.Fatalf("Messages = %+v, want [m2]", resp.Messages)
		}
		if resp.NextOffset != 1 || resp.HasMore {
			t.Errorf("envelope = %+v, want nextOffset=1 hasMore=false", resp)
		}
	})

	t.Run("no match", func(t *testing.T) {
		id := "nosuch"
		resp, rf, err := srv.readMessagePage(context.Background(), mockIdentityKey, "inbox", listPagination{Limit: 1000, Offset: 0}, &id, -1)
		if err != nil || rf != nil {
			t.Fatalf("err=%v rf=%+v", err, rf)
		}
		if len(resp.Messages) != 0 {
			t.Fatalf("Messages = %+v, want empty", resp.Messages)
		}
		if resp.NextOffset != 0 || resp.HasMore {
			t.Errorf("envelope = %+v, want nextOffset=0 hasMore=false", resp)
		}
	})
}

// messageJSONBytes returns the exact encoded size readMessagePage's byte
// budget would charge one message of this shape, so budget-boundary tests
// aren't tied to an assumed encoding width.
func messageJSONBytes(t *testing.T, m MessageOut) int {
	t.Helper()
	// marshalJSONNoEscape, not json.Marshal: readMessagePage counts bytes the
	// way TS's JSON.stringify would (no HTML-escaping of '<', '>', '&'), so this
	// helper must mirror that exactly rather than encoding/json's default.
	b, err := marshalJSONNoEscape(m)
	if err != nil {
		t.Fatal(err)
	}
	return len(b) + 1
}

func TestReadMessagePage_OversizedFirstMessageIs413(t *testing.T) {
	srv := setupTestServer(t)
	insertMsgs(t, srv, mockIdentityKey, "inbox", []string{"m1", "m2"})

	// The fixed overhead alone (256) already exceeds this budget, so even the
	// smallest possible first message cannot fit.
	resp, rf, err := srv.readMessagePage(context.Background(), mockIdentityKey, "inbox", listPagination{Limit: 1000, Offset: 0}, nil, 100)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if rf == nil || rf.code != "ERR_MESSAGE_RESPONSE_TOO_LARGE" || rf.status != 413 {
		t.Fatalf("rf = %+v, want 413 ERR_MESSAGE_RESPONSE_TOO_LARGE", rf)
	}
	if resp.Messages != nil || resp.Status != "" {
		t.Errorf("resp = %+v, want zero value on failure", resp)
	}
}

func TestReadMessagePage_ByteBudgetTruncatesPage(t *testing.T) {
	srv := setupTestServer(t)
	// Same-length ids/sender/body so every formatted message encodes to the
	// same byte width, making the budget arithmetic exact.
	insertMsgs(t, srv, mockIdentityKey, "inbox", []string{"aaa", "bbb", "ccc"})

	sample := MessageOut{MessageID: "aaa", Body: "aaa", Sender: "02sender", CreatedAt: formatTime(srv.clock()), UpdatedAt: formatTime(srv.clock())}
	itemBytes := messageJSONBytes(t, sample)
	budget := listMessagesResponseByteOverhead + 2*itemBytes // room for exactly two

	resp, rf, err := srv.readMessagePage(context.Background(), mockIdentityKey, "inbox", listPagination{Limit: 1000, Offset: 0}, nil, budget)
	if err != nil || rf != nil {
		t.Fatalf("err=%v rf=%+v", err, rf)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("got %d messages, want 2 (budget fits exactly two)", len(resp.Messages))
	}
	if resp.Messages[0].MessageID != "aaa" || resp.Messages[1].MessageID != "bbb" {
		t.Errorf("Messages = %+v, want [aaa bbb]", resp.Messages)
	}
	if resp.NextOffset != 2 || !resp.HasMore {
		t.Errorf("envelope = %+v, want nextOffset=2 hasMore=true", resp)
	}
}

// TestReadMessagePage_HTMLCharsDoNotInflateByteBudget pins the fix for
// encoding/json's default HTML-escaping: '<', '>' and '&' become <,
// > and & (6 bytes apiece) under json.Marshal's default settings,
// but TS's Buffer.byteLength(JSON.stringify(formatted), 'utf8') — what the
// byte budget must match — counts each as a single byte, since
// JSON.stringify never HTML-escapes. A budget sized to fit the correct
// (no-escape) encoding of a message containing these characters, but too
// small for the escaped encoding, must still return that message with
// hasMore=false.
func TestReadMessagePage_HTMLCharsDoNotInflateByteBudget(t *testing.T) {
	srv := setupTestServer(t)
	const body = "<b>hi & bye</b>"
	if err := srv.Store.InsertMessage(context.Background(), storage.NewMessage{
		MessageID: "m1", Recipient: mockIdentityKey, MessageBox: "inbox", Sender: "02sender", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	sample := MessageOut{MessageID: "m1", Body: body, Sender: "02sender", CreatedAt: formatTime(srv.clock()), UpdatedAt: formatTime(srv.clock())}
	itemBytes := messageJSONBytes(t, sample) // the correct, no-escape byte count
	budget := listMessagesResponseByteOverhead + itemBytes

	// Sanity check that this test would actually catch the regression: the old
	// HTML-escaping encoding must be strictly larger than the budget above, or
	// this test would pass even without the fix.
	escaped, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(escaped)+1 <= itemBytes {
		t.Fatalf("test setup invalid: HTML-escaped encoding (%d bytes) is not larger than the no-escape one (%d bytes)", len(escaped)+1, itemBytes)
	}

	resp, rf, err := srv.readMessagePage(context.Background(), mockIdentityKey, "inbox", listPagination{Limit: 1000, Offset: 0}, nil, budget)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if rf != nil {
		t.Fatalf("rf = %+v, want nil: the message fits the no-escape budget exactly", rf)
	}
	if len(resp.Messages) != 1 || resp.HasMore {
		t.Errorf("resp = %+v, want exactly 1 message and hasMore=false", resp)
	}
	if len(resp.Messages) == 1 && resp.Messages[0].Body != body {
		t.Errorf("Body = %q, want %q (raw, not HTML-escaped)", resp.Messages[0].Body, body)
	}
}

// fetchMessagePageRows must pick up a store that implements storage.MessagePager
// (sqlstore does) rather than only ever exercising the fallback.
func TestReadMessagePage_UsesPagerWhenStoreImplementsIt(t *testing.T) {
	store, err := sqlstore.New("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := any(store).(storage.MessagePager); !ok {
		t.Fatal("sqlstore.Store must implement storage.MessagePager for this test to mean anything")
	}

	srv := &Server{Store: store, listMessages: defaultListMessagesConfig()}
	insertMsgs(t, srv, mockIdentityKey, "inbox", []string{"p1", "p2", "p3"})

	resp, rf, err := srv.readMessagePage(context.Background(), mockIdentityKey, "inbox", listPagination{Limit: 2, Offset: 0}, nil, -1)
	if err != nil || rf != nil {
		t.Fatalf("err=%v rf=%+v", err, rf)
	}
	if len(resp.Messages) != 2 || resp.Messages[0].MessageID != "p1" || resp.Messages[1].MessageID != "p2" {
		t.Fatalf("Messages = %+v, want [p1 p2]", resp.Messages)
	}
	if !resp.HasMore || resp.NextOffset != 2 {
		t.Errorf("envelope = %+v, want hasMore=true nextOffset=2", resp)
	}
}

// A storage failure must become a plain error (so the caller returns 500), not
// a *routeFailure.
func TestReadMessagePage_StorageErrorPropagates(t *testing.T) {
	srv := setupTestServer(t)
	wantErr := errors.New("boom")
	srv.Store = &erroringListStore{fakeStore: srv.Store.(*fakeStore), err: wantErr}

	_, rf, err := srv.readMessagePage(context.Background(), mockIdentityKey, "inbox", listPagination{Limit: 10, Offset: 0}, nil, -1)
	if rf != nil {
		t.Fatalf("rf = %+v, want nil", rf)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

// erroringListStore wraps fakeStore and fails ListMessages, exercising
// readMessagePage's error path through the fallback (non-pager) route.
type erroringListStore struct {
	*fakeStore
	err error
}

func (e *erroringListStore) ListMessages(context.Context, string, string) ([]storage.Message, error) {
	return nil, e.err
}

func TestFallbackPageMessages(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := store.InsertMessage(ctx, storage.NewMessage{MessageID: id, Recipient: "alice", MessageBox: "inbox", Sender: "s", Body: id}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("offset within range", func(t *testing.T) {
		got, err := fallbackPageMessages(ctx, store, "alice", "inbox", 1, 2, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].MessageID != "b" || got[1].MessageID != "c" {
			t.Errorf("got %v, want [b c]", messageIDsOf(got))
		}
	})

	t.Run("offset past end", func(t *testing.T) {
		got, err := fallbackPageMessages(ctx, store, "alice", "inbox", 100, 2, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("got %v, want empty", messageIDsOf(got))
		}
	})

	t.Run("fetchLimit beyond remaining is clamped", func(t *testing.T) {
		got, err := fallbackPageMessages(ctx, store, "alice", "inbox", 2, 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].MessageID != "c" || got[1].MessageID != "d" {
			t.Errorf("got %v, want [c d]", messageIDsOf(got))
		}
	})

	t.Run("unknown recipient", func(t *testing.T) {
		got, err := fallbackPageMessages(ctx, store, "nobody", "inbox", 0, 10, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("got %v, want empty", messageIDsOf(got))
		}
	})
}

func messageIDsOf(msgs []storage.Message) []string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.MessageID
	}
	return ids
}

func TestBoundedInteger(t *testing.T) {
	cases := []struct {
		name   string
		v      any
		min    int
		max    int
		want   int
		wantOK bool
	}{
		{"in range", float64(5), 1, 10, 5, true},
		{"at min", float64(0), 0, 10, 0, true},
		{"below min", float64(-1), 0, 10, 0, false},
		{"above max", float64(11), 0, 10, 0, false},
		{"unlimited max", float64(1_000_000), 0, -1, 1_000_000, true},
		{"non-integer", 1.5, 0, 10, 0, false},
		{"wrong type string", "5", 0, 10, 0, false},
		{"wrong type bool", true, 0, 10, 0, false},
		{"wrong type nil", nil, 0, 10, 0, false},
		{"beyond safe integer", float64(int64(1) << 60), 0, -1, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := boundedInteger(c.v, c.min, c.max)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestTrimmedEmpty(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"", true},
		{"   ", true},
		{"\t\n", true},
		{"inbox", false},
		{" inbox", false},
		// U+0085 (NEL) is a control character, not JS whitespace: a value that is
		// only NEL must be reported as invalid (control char), not as blank.
		{"\u0085", false},
	}
	for _, c := range cases {
		if got := trimmedEmpty(c.s); got != c.want {
			t.Errorf("trimmedEmpty(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}
