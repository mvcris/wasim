package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/store"
)

func openMemory(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), store.MemoryPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestStateSurvivesReopeningTheFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state with space.db")

	first, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.EnsureContact(ctx, "5511", "Ana", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopening must not re-run migrations: %v", err)
	}
	defer second.Close()
	contact, err := second.GetContact(ctx, "5511")
	if err != nil {
		t.Fatal(err)
	}
	if contact.Name != "Ana" {
		t.Errorf("Name = %q", contact.Name)
	}
}

func TestEnsureContactKeepsTheNameWhenNoneIsGiven(t *testing.T) {
	ctx := context.Background()
	database := openMemory(t)
	if _, err := database.EnsureContact(ctx, "5511", "Ana", time.Now()); err != nil {
		t.Fatal(err)
	}
	contact, err := database.EnsureContact(ctx, "5511", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if contact.Name != "Ana" {
		t.Errorf("Name = %q, want Ana", contact.Name)
	}
}

func TestTouchInboundOnlyMovesForward(t *testing.T) {
	ctx := context.Background()
	database := openMemory(t)
	now := time.Now()
	if _, err := database.EnsureContact(ctx, "5511", "Ana", now); err != nil {
		t.Fatal(err)
	}
	if err := database.TouchInbound(ctx, "5511", now); err != nil {
		t.Fatal(err)
	}
	if err := database.TouchInbound(ctx, "5511", now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	contact, err := database.GetContact(ctx, "5511")
	if err != nil {
		t.Fatal(err)
	}
	if !contact.WindowOpen(now) {
		t.Error("a back-dated message closed an open window")
	}
}

func TestInTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	database := openMemory(t)
	boom := errors.New("boom")
	err := database.InTx(ctx, func(q *store.Queries) error {
		if _, err := q.EnsureContact(ctx, "5511", "Ana", time.Now()); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := database.GetContact(ctx, "5511"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("contact survived the rollback: %v", err)
	}
}

func insertOutbound(t *testing.T, database *store.Store, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := database.EnsureContact(ctx, "5511", "Ana", time.Now()); err != nil {
		t.Fatal(err)
	}
	message := domain.Message{
		ID:          id,
		ContactWaID: "5511",
		Direction:   domain.DirectionOutbound,
		Type:        "text",
		Status:      domain.StatusAccepted,
		Rendered:    cloudapi.Rendered{Body: "hi"},
		Raw:         []byte(`{}`),
		Timestamp:   time.Now(),
	}
	if err := database.InsertMessage(ctx, &message); err != nil {
		t.Fatal(err)
	}
}

func TestApplyMessageStatusFollowsTheProgression(t *testing.T) {
	tests := []struct {
		name  string
		steps []domain.MessageStatus
		want  domain.MessageStatus
	}{
		{"in order", []domain.MessageStatus{domain.StatusSent, domain.StatusDelivered, domain.StatusRead}, domain.StatusRead},
		{"out of order keeps the furthest", []domain.MessageStatus{domain.StatusRead, domain.StatusDelivered, domain.StatusSent}, domain.StatusRead},
		{"read without delivered", []domain.MessageStatus{domain.StatusSent, domain.StatusRead}, domain.StatusRead},
		{"failed after delivered", []domain.MessageStatus{domain.StatusDelivered, domain.StatusFailed}, domain.StatusFailed},
		{"failed is terminal", []domain.MessageStatus{domain.StatusFailed, domain.StatusRead}, domain.StatusFailed},
		{"read beats a late failure", []domain.MessageStatus{domain.StatusRead, domain.StatusFailed}, domain.StatusRead},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database := openMemory(t)
			insertOutbound(t, database, "wamid.x")
			for _, step := range test.steps {
				if err := database.ApplyMessageStatus(ctx, "wamid.x", step, 131026, "Title"); err != nil {
					t.Fatal(err)
				}
			}
			message, err := database.GetMessage(ctx, "wamid.x")
			if err != nil {
				t.Fatal(err)
			}
			if message.Status != test.want {
				t.Errorf("Status = %q, want %q", message.Status, test.want)
			}
			if wantCode := test.want == domain.StatusFailed; (message.ErrorCode != 0) != wantCode {
				t.Errorf("ErrorCode = %d with status %q", message.ErrorCode, message.Status)
			}
		})
	}
}

func TestConsumeFailureRule(t *testing.T) {
	ctx := context.Background()
	add := func(database *store.Store, waID string, code, remaining int) {
		t.Helper()
		_, err := database.InsertFailureRule(ctx, domain.FailureRule{
			ContactWaID: waID, Code: code, Channel: cloudapi.ChannelAsync, Remaining: remaining, CreatedAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	consume := func(database *store.Store, waID string) int {
		t.Helper()
		rule, err := database.ConsumeFailureRule(ctx, waID)
		if err != nil {
			t.Fatal(err)
		}
		if rule == nil {
			return 0
		}
		return rule.Code
	}

	t.Run("counts down and disappears", func(t *testing.T) {
		database := openMemory(t)
		add(database, "5511", 100, 2)
		for attempt, want := range []int{100, 100, 0} {
			if got := consume(database, "5511"); got != want {
				t.Errorf("attempt %d = %d, want %d", attempt, got, want)
			}
		}
	})
	t.Run("sticky never runs out", func(t *testing.T) {
		database := openMemory(t)
		add(database, "5511", 100, domain.RuleSticky)
		for attempt := range 3 {
			if got := consume(database, "5511"); got != 100 {
				t.Errorf("attempt %d = %d", attempt, got)
			}
		}
	})
	t.Run("a contact's rule wins over a global one", func(t *testing.T) {
		database := openMemory(t)
		add(database, "", 100, 1)
		add(database, "5511", 200, 1)
		if got := consume(database, "5511"); got != 200 {
			t.Errorf("got %d, want the contact's rule", got)
		}
		if got := consume(database, "5511"); got != 100 {
			t.Errorf("got %d, want the global rule next", got)
		}
	})
	t.Run("another contact's rule does not apply", func(t *testing.T) {
		database := openMemory(t)
		add(database, "5522", 100, 1)
		if got := consume(database, "5511"); got != 0 {
			t.Errorf("got %d", got)
		}
	})
}

func TestClaimDeliveryAppliesTheStatusOnlyOnce(t *testing.T) {
	ctx := context.Background()
	database := openMemory(t)
	insertOutbound(t, database, "wamid.x")
	now := time.Now()
	err := database.EnqueueDelivery(ctx, domain.Delivery{
		Kind: domain.DeliveryKindStatus, Body: []byte(`{}`), NextAttemptAt: now, CreatedAt: now,
		MessageID: "wamid.x", MessageStatus: domain.StatusDelivered,
	})
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := database.ClaimDelivery(ctx, now)
	if err != nil || claimed == nil {
		t.Fatalf("claimed = %v, err = %v", claimed, err)
	}
	if err := database.ApplyMessageStatus(ctx, "wamid.x", domain.StatusRead, 0, ""); err != nil {
		t.Fatal(err)
	}

	if err := database.RetryDelivery(ctx, claimed.ID, 500, "down", now); err != nil {
		t.Fatal(err)
	}
	again, err := database.ClaimDelivery(ctx, now)
	if err != nil || again == nil {
		t.Fatalf("second claim = %v, err = %v", again, err)
	}
	if again.Attempts != 2 {
		t.Errorf("Attempts = %d", again.Attempts)
	}
	message, err := database.GetMessage(ctx, "wamid.x")
	if err != nil {
		t.Fatal(err)
	}
	if message.Status != domain.StatusRead {
		t.Errorf("Status = %q", message.Status)
	}

	empty, err := database.ClaimDelivery(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if empty != nil {
		t.Errorf("a delivery in flight was claimed twice: %+v", empty)
	}
}

func TestReleaseInFlightRequeuesInterruptedWork(t *testing.T) {
	ctx := context.Background()
	database := openMemory(t)
	now := time.Now()
	err := database.EnqueueDelivery(ctx, domain.Delivery{Kind: domain.DeliveryKindMessage, Body: []byte(`{}`), NextAttemptAt: now, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimDelivery(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := database.ReleaseInFlight(ctx, now); err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimDelivery(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil {
		t.Error("the interrupted webhook was not requeued")
	}
}

func TestListTemplatesPagesInBothDirections(t *testing.T) {
	ctx := context.Background()
	database := openMemory(t)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		err := database.UpsertTemplate(ctx, cloudapi.TemplateDefinition{
			ID: name, Name: name, Language: "en_US", Status: "APPROVED", Category: "UTILITY",
			ParameterFormat: "POSITIONAL", Components: []cloudapi.TemplateComponent{{Type: "BODY", Text: name}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	names := func(page store.TemplatePage) string {
		joined := ""
		for _, template := range page.Templates {
			joined += template.Name
		}
		return joined
	}

	first, err := database.ListTemplates(ctx, store.TemplateFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if names(first) != "ab" || first.HasBefore || !first.HasAfter {
		t.Errorf("first page = %q, before=%v after=%v", names(first), first.HasBefore, first.HasAfter)
	}

	second, err := database.ListTemplates(ctx, store.TemplateFilter{Limit: 2, After: first.LastSeq})
	if err != nil {
		t.Fatal(err)
	}
	if names(second) != "cd" || !second.HasBefore || !second.HasAfter {
		t.Errorf("second page = %q, before=%v after=%v", names(second), second.HasBefore, second.HasAfter)
	}

	third, err := database.ListTemplates(ctx, store.TemplateFilter{Limit: 2, After: second.LastSeq})
	if err != nil {
		t.Fatal(err)
	}
	if names(third) != "e" || third.HasAfter {
		t.Errorf("third page = %q, after=%v", names(third), third.HasAfter)
	}

	back, err := database.ListTemplates(ctx, store.TemplateFilter{Limit: 2, Before: third.FirstSeq})
	if err != nil {
		t.Fatal(err)
	}
	if names(back) != "cd" {
		t.Errorf("walking back = %q, want the rows nearest the cursor in order", names(back))
	}
}

func TestUpsertTemplateReplacesByNameAndLanguage(t *testing.T) {
	ctx := context.Background()
	database := openMemory(t)
	template := cloudapi.TemplateDefinition{ID: "1", Name: "a", Language: "en_US", Status: "PENDING", Category: "UTILITY", ParameterFormat: "POSITIONAL"}
	if err := database.UpsertTemplate(ctx, template); err != nil {
		t.Fatal(err)
	}
	template.Status = "APPROVED"
	if err := database.UpsertTemplate(ctx, template); err != nil {
		t.Fatal(err)
	}
	found, err := database.FindTemplate(ctx, "a", "en_US")
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != "APPROVED" {
		t.Errorf("Status = %q", found.Status)
	}
	if _, err := database.FindTemplate(ctx, "a", "pt_BR"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("other language: %v", err)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	got, err := store.DecodeCursor(store.EncodeCursor(1234))
	if err != nil {
		t.Fatal(err)
	}
	if got != 1234 {
		t.Errorf("got %d", got)
	}
	if _, err := store.DecodeCursor("!!!"); err == nil {
		t.Error("a malformed cursor was accepted")
	}
}
