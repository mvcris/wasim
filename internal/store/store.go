// Package store persists the simulator state in SQLite.
package store

import (
	"cmp"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/domain"

	_ "modernc.org/sqlite" // Registers the pure Go "sqlite" driver.
)

// MemoryPath keeps the database in memory, so state starts empty on every run.
const MemoryPath = ":memory:"

//go:embed migrations/*.sql
var migrationFiles embed.FS

type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Queries runs the data access either on the database or inside a transaction.
type Queries struct {
	db dbtx
}

// Store owns the database connection.
type Store struct {
	*Queries
	db *sql.DB
}

// Open opens the database at path and applies pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One connection serializes writers, which SQLite requires anyway, and it
	// is the only way ":memory:" stays one database instead of one per
	// connection. Code running inside InTx must use the Queries it receives,
	// or it will wait forever for the connection the transaction holds.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	store := &Store{Queries: &Queries{db: db}, db: db}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func dataSourceName(path string) string {
	pragmas := url.Values{}
	pragmas.Add("_pragma", "foreign_keys(1)")
	pragmas.Add("_pragma", "busy_timeout(5000)")
	if path == MemoryPath {
		return "file::memory:?" + pragmas.Encode()
	}
	pragmas.Add("_pragma", "journal_mode(WAL)")
	location := url.URL{Scheme: "file", Opaque: path, RawQuery: pragmas.Encode()}
	return location.String()
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// InTx runs fn in a transaction, committing only if it returns nil.
func (s *Store) InTx(ctx context.Context, fn func(*Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(&Queries{db: tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	for _, name := range names {
		if err := s.applyMigration(ctx, name); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, name string) error {
	return s.InTx(ctx, func(q *Queries) error {
		var applied int
		row := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name)
		if err := row.Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			return nil
		}
		script, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err := q.db.ExecContext(ctx, string(script)); err != nil {
			return err
		}
		_, err = q.db.ExecContext(ctx, `INSERT INTO schema_migrations (name) VALUES (?)`, name)
		return err
	})
}

// Contacts.

// EnsureContact returns the contact, creating it if needed. A non-empty name
// replaces the stored one.
func (q *Queries) EnsureContact(ctx context.Context, waID, name string, now time.Time) (domain.Contact, error) {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO contacts (wa_id, name, created_at) VALUES (?, ?, ?)
		ON CONFLICT (wa_id) DO UPDATE SET name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE name END`,
		waID, name, now.UnixMilli())
	if err != nil {
		return domain.Contact{}, fmt.Errorf("upsert contact: %w", err)
	}
	return q.GetContact(ctx, waID)
}

// GetContact returns one contact.
func (q *Queries) GetContact(ctx context.Context, waID string) (domain.Contact, error) {
	row := q.db.QueryRowContext(ctx, `SELECT wa_id, name, last_inbound_at, created_at FROM contacts WHERE wa_id = ?`, waID)
	contact, err := scanContact(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Contact{}, domain.ErrNotFound
	}
	return contact, err
}

// ListContacts returns contacts, most recently active first.
func (q *Queries) ListContacts(ctx context.Context) ([]domain.Contact, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT c.wa_id, c.name, c.last_inbound_at, c.created_at
		FROM contacts c
		LEFT JOIN (SELECT contact_wa_id, MAX(seq) AS last_seq FROM messages GROUP BY contact_wa_id) m
			ON m.contact_wa_id = c.wa_id
		ORDER BY COALESCE(m.last_seq, 0) DESC, c.created_at DESC, c.wa_id`)
	if err != nil {
		return nil, fmt.Errorf("list contacts: %w", err)
	}
	defer rows.Close()
	contacts := []domain.Contact{}
	for rows.Next() {
		contact, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, contact)
	}
	return contacts, rows.Err()
}

// TouchInbound records a user message, moving the window forward only: a
// back-dated message must not close a window a newer one opened.
func (q *Queries) TouchInbound(ctx context.Context, waID string, at time.Time) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE contacts SET last_inbound_at = MAX(COALESCE(last_inbound_at, 0), ?) WHERE wa_id = ?`,
		at.UnixMilli(), waID)
	return err
}

// ClearWindow closes the contact's customer service window.
func (q *Queries) ClearWindow(ctx context.Context, waID string) error {
	result, err := q.db.ExecContext(ctx, `UPDATE contacts SET last_inbound_at = NULL WHERE wa_id = ?`, waID)
	if err != nil {
		return err
	}
	return requireRow(result)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanContact(row scanner) (domain.Contact, error) {
	var contact domain.Contact
	var lastInbound sql.NullInt64
	var created int64
	if err := row.Scan(&contact.WaID, &contact.Name, &lastInbound, &created); err != nil {
		return domain.Contact{}, err
	}
	contact.CreatedAt = time.UnixMilli(created).UTC()
	contact.LastInboundAt = nullableTime(lastInbound)
	return contact, nil
}

// Messages.

// InsertMessage stores a message and fills in its sequence number.
func (q *Queries) InsertMessage(ctx context.Context, message *domain.Message) error {
	rendered, err := json.Marshal(message.Rendered)
	if err != nil {
		return fmt.Errorf("encode rendered message: %w", err)
	}
	result, err := q.db.ExecContext(ctx, `
		INSERT INTO messages (id, contact_wa_id, direction, type, status, rendered, raw, context_id, media_id, category, error_code, error_title, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		message.ID, message.ContactWaID, message.Direction, message.Type, message.Status,
		string(rendered), string(message.Raw), message.ContextID, message.MediaID, message.Category,
		message.ErrorCode, message.ErrorTitle, message.Timestamp.UnixMilli())
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	message.Seq, err = result.LastInsertId()
	return err
}

const messageColumns = `seq, id, contact_wa_id, direction, type, status, rendered, raw, context_id, media_id, category, error_code, error_title, timestamp`

// GetMessage returns one message by wamid.
func (q *Queries) GetMessage(ctx context.Context, id string) (domain.Message, error) {
	row := q.db.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE id = ?`, id)
	message, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Message{}, domain.ErrNotFound
	}
	return message, err
}

// ListMessages returns a conversation, oldest first.
func (q *Queries) ListMessages(ctx context.Context, waID string) ([]domain.Message, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE contact_wa_id = ? ORDER BY seq`, waID)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()
	messages := []domain.Message{}
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

// ApplyMessageStatus moves a message forward through its status progression.
func (q *Queries) ApplyMessageStatus(ctx context.Context, id string, next domain.MessageStatus, errorCode int, errorTitle string) error {
	message, err := q.GetMessage(ctx, id)
	if err != nil {
		return err
	}
	advanced := message.Status.Advance(next)
	if advanced == message.Status {
		return nil
	}
	if advanced != domain.StatusFailed {
		errorCode, errorTitle = 0, ""
	}
	_, err = q.db.ExecContext(ctx, `UPDATE messages SET status = ?, error_code = ?, error_title = ? WHERE id = ?`,
		advanced, errorCode, errorTitle, id)
	return err
}

// MarkInboundRead marks a user message and every earlier one in its
// conversation as read, which is what the mark-as-read endpoint documents.
func (q *Queries) MarkInboundRead(ctx context.Context, message domain.Message) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE messages SET status = ?
		WHERE contact_wa_id = ? AND direction = ? AND seq <= ?`,
		domain.StatusRead, message.ContactWaID, domain.DirectionInbound, message.Seq)
	return err
}

func scanMessage(row scanner) (domain.Message, error) {
	var message domain.Message
	var rendered, raw string
	var timestamp int64
	err := row.Scan(&message.Seq, &message.ID, &message.ContactWaID, &message.Direction, &message.Type, &message.Status,
		&rendered, &raw, &message.ContextID, &message.MediaID, &message.Category, &message.ErrorCode, &message.ErrorTitle, &timestamp)
	if err != nil {
		return domain.Message{}, err
	}
	if err := json.Unmarshal([]byte(rendered), &message.Rendered); err != nil {
		return domain.Message{}, fmt.Errorf("decode rendered message: %w", err)
	}
	message.Raw = json.RawMessage(raw)
	message.Timestamp = time.UnixMilli(timestamp).UTC()
	return message, nil
}

// Media.

// InsertMedia stores a media record.
func (q *Queries) InsertMedia(ctx context.Context, media domain.Media) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO media (id, mime_type, sha256, file_size, filename, path, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		media.ID, media.MimeType, media.SHA256, media.FileSize, media.Filename, media.Path, media.CreatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("insert media: %w", err)
	}
	return nil
}

// GetMedia returns one media record.
func (q *Queries) GetMedia(ctx context.Context, id string) (domain.Media, error) {
	var media domain.Media
	var created int64
	row := q.db.QueryRowContext(ctx, `SELECT id, mime_type, sha256, file_size, filename, path, created_at FROM media WHERE id = ?`, id)
	err := row.Scan(&media.ID, &media.MimeType, &media.SHA256, &media.FileSize, &media.Filename, &media.Path, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Media{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Media{}, err
	}
	media.CreatedAt = time.UnixMilli(created).UTC()
	return media, nil
}

// Templates.

// UpsertTemplate adds a template or replaces the one with the same name and
// language.
func (q *Queries) UpsertTemplate(ctx context.Context, template cloudapi.TemplateDefinition) error {
	components, err := json.Marshal(template.Components)
	if err != nil {
		return fmt.Errorf("encode template components: %w", err)
	}
	_, err = q.db.ExecContext(ctx, `
		INSERT INTO templates (id, name, language, status, category, parameter_format, components)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (name, language) DO UPDATE SET
			status = excluded.status,
			category = excluded.category,
			parameter_format = excluded.parameter_format,
			components = excluded.components`,
		template.ID, template.Name, template.Language, template.Status, template.Category,
		template.ParameterFormat, string(components))
	if err != nil {
		return fmt.Errorf("upsert template: %w", err)
	}
	return nil
}

// FindTemplate returns the template with this name and language.
func (q *Queries) FindTemplate(ctx context.Context, name, language string) (cloudapi.TemplateDefinition, error) {
	row := q.db.QueryRowContext(ctx, `SELECT seq, `+templateColumns+` FROM templates WHERE name = ? AND language = ?`, name, language)
	_, template, err := scanTemplate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return cloudapi.TemplateDefinition{}, domain.ErrNotFound
	}
	return template, err
}

const templateColumns = `id, name, language, status, category, parameter_format, components`

// TemplateFilter selects a page of the catalog.
type TemplateFilter struct {
	Name   string
	Status string
	After  int64
	Before int64
	Limit  int
}

// TemplatePage is one page of the catalog, with the keyset positions of its
// edges and whether more rows exist on each side.
type TemplatePage struct {
	Templates []cloudapi.TemplateDefinition
	FirstSeq  int64
	LastSeq   int64
	HasBefore bool
	HasAfter  bool
}

// ListTemplates returns one page of the catalog in insertion order.
func (q *Queries) ListTemplates(ctx context.Context, filter TemplateFilter) (TemplatePage, error) {
	where := []string{"1 = 1"}
	args := []any{}
	if filter.Name != "" {
		where = append(where, "name = ?")
		args = append(args, filter.Name)
	}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, strings.ToUpper(filter.Status))
	}
	base := strings.Join(where, " AND ")

	pageWhere, pageArgs, order := base, slices.Clone(args), "seq ASC"
	switch {
	case filter.After > 0:
		pageWhere += " AND seq > ?"
		pageArgs = append(pageArgs, filter.After)
	case filter.Before > 0:
		// Walking backwards needs the rows nearest the cursor, then re-sorting.
		pageWhere += " AND seq < ?"
		pageArgs = append(pageArgs, filter.Before)
		order = "seq DESC"
	}
	pageArgs = append(pageArgs, filter.Limit)
	rows, err := q.db.QueryContext(ctx,
		`SELECT seq, `+templateColumns+` FROM templates WHERE `+pageWhere+` ORDER BY `+order+` LIMIT ?`, pageArgs...)
	if err != nil {
		return TemplatePage{}, fmt.Errorf("list templates: %w", err)
	}
	defer rows.Close()

	type entry struct {
		seq      int64
		template cloudapi.TemplateDefinition
	}
	entries := []entry{}
	for rows.Next() {
		seq, template, err := scanTemplate(rows)
		if err != nil {
			return TemplatePage{}, err
		}
		entries = append(entries, entry{seq, template})
	}
	if err := rows.Err(); err != nil {
		return TemplatePage{}, err
	}
	// The connection is needed again below, and the pool has only one.
	_ = rows.Close()
	slices.SortFunc(entries, func(a, b entry) int { return cmp.Compare(a.seq, b.seq) })

	page := TemplatePage{Templates: make([]cloudapi.TemplateDefinition, 0, len(entries))}
	for _, item := range entries {
		page.Templates = append(page.Templates, item.template)
	}
	if len(entries) == 0 {
		return page, nil
	}
	page.FirstSeq, page.LastSeq = entries[0].seq, entries[len(entries)-1].seq
	if page.HasBefore, err = q.templateExists(ctx, base, args, "seq < ?", page.FirstSeq); err != nil {
		return TemplatePage{}, err
	}
	if page.HasAfter, err = q.templateExists(ctx, base, args, "seq > ?", page.LastSeq); err != nil {
		return TemplatePage{}, err
	}
	return page, nil
}

func (q *Queries) templateExists(ctx context.Context, base string, args []any, condition string, seq int64) (bool, error) {
	var count int
	row := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM templates WHERE `+base+` AND `+condition,
		append(slices.Clone(args), seq)...)
	if err := row.Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func scanTemplate(row scanner) (int64, cloudapi.TemplateDefinition, error) {
	var seq int64
	var template cloudapi.TemplateDefinition
	var components string
	err := row.Scan(&seq, &template.ID, &template.Name, &template.Language, &template.Status,
		&template.Category, &template.ParameterFormat, &components)
	if err != nil {
		return 0, cloudapi.TemplateDefinition{}, err
	}
	if err := json.Unmarshal([]byte(components), &template.Components); err != nil {
		return 0, cloudapi.TemplateDefinition{}, fmt.Errorf("decode template components: %w", err)
	}
	return seq, template, nil
}

// Failure rules.

// InsertFailureRule adds a failure rule.
func (q *Queries) InsertFailureRule(ctx context.Context, rule domain.FailureRule) (domain.FailureRule, error) {
	result, err := q.db.ExecContext(ctx, `
		INSERT INTO failure_rules (contact_wa_id, code, channel, remaining, created_at) VALUES (?, ?, ?, ?, ?)`,
		rule.ContactWaID, rule.Code, rule.Channel, rule.Remaining, rule.CreatedAt.UnixMilli())
	if err != nil {
		return domain.FailureRule{}, fmt.Errorf("insert failure rule: %w", err)
	}
	rule.ID, err = result.LastInsertId()
	return rule, err
}

// ListFailureRules returns the rules still in effect, oldest first.
func (q *Queries) ListFailureRules(ctx context.Context) ([]domain.FailureRule, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, contact_wa_id, code, channel, remaining, created_at FROM failure_rules ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list failure rules: %w", err)
	}
	defer rows.Close()
	rules := []domain.FailureRule{}
	for rows.Next() {
		var rule domain.FailureRule
		var created int64
		if err := rows.Scan(&rule.ID, &rule.ContactWaID, &rule.Code, &rule.Channel, &rule.Remaining, &created); err != nil {
			return nil, err
		}
		rule.CreatedAt = time.UnixMilli(created).UTC()
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

// DeleteFailureRule removes a rule.
func (q *Queries) DeleteFailureRule(ctx context.Context, id int64) error {
	result, err := q.db.ExecContext(ctx, `DELETE FROM failure_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(result)
}

// ConsumeFailureRule returns the oldest rule matching the contact and counts one
// use against it. A contact-specific rule wins over a global one. Returns nil
// when no rule applies.
func (q *Queries) ConsumeFailureRule(ctx context.Context, waID string) (*domain.FailureRule, error) {
	var rule domain.FailureRule
	row := q.db.QueryRowContext(ctx, `
		SELECT id, contact_wa_id, code, channel, remaining FROM failure_rules
		WHERE contact_wa_id = ? OR contact_wa_id = ''
		ORDER BY contact_wa_id = '' ASC, id ASC LIMIT 1`, waID)
	err := row.Scan(&rule.ID, &rule.ContactWaID, &rule.Code, &rule.Channel, &rule.Remaining)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find failure rule: %w", err)
	}
	if rule.Remaining == domain.RuleSticky {
		return &rule, nil
	}
	if rule.Remaining <= 1 {
		_, err = q.db.ExecContext(ctx, `DELETE FROM failure_rules WHERE id = ?`, rule.ID)
	} else {
		_, err = q.db.ExecContext(ctx, `UPDATE failure_rules SET remaining = remaining - 1 WHERE id = ?`, rule.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("consume failure rule: %w", err)
	}
	return &rule, nil
}

// Webhook deliveries.

const deliveryColumns = `id, kind, state, body, attempts, next_attempt_at, last_status, last_error, message_id, message_status, error_code, error_title, bad_signature, created_at, finished_at`

// EnqueueDelivery adds a webhook to the outbox.
func (q *Queries) EnqueueDelivery(ctx context.Context, delivery domain.Delivery) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (kind, state, body, next_attempt_at, message_id, message_status, error_code, error_title, bad_signature, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		delivery.Kind, domain.DeliveryPending, delivery.Body, delivery.NextAttemptAt.UnixMilli(),
		delivery.MessageID, delivery.MessageStatus, delivery.ErrorCode, delivery.ErrorTitle,
		boolToInt(delivery.BadSignature), delivery.CreatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("enqueue delivery: %w", err)
	}
	return nil
}

// ClaimDelivery takes the next due webhook, marking it in flight. On the first
// attempt of a status webhook it also applies the status to the message. It
// returns nil when nothing is due.
func (s *Store) ClaimDelivery(ctx context.Context, now time.Time) (*domain.Delivery, error) {
	var claimed *domain.Delivery
	err := s.InTx(ctx, func(q *Queries) error {
		row := q.db.QueryRowContext(ctx, `
			UPDATE webhook_deliveries SET state = ?, attempts = attempts + 1
			WHERE id = (
				SELECT id FROM webhook_deliveries
				WHERE state = ? AND next_attempt_at <= ?
				ORDER BY next_attempt_at, id LIMIT 1
			)
			RETURNING `+deliveryColumns,
			domain.DeliveryInFlight, domain.DeliveryPending, now.UnixMilli())
		delivery, err := scanDelivery(row)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim delivery: %w", err)
		}
		claimed = &delivery
		if delivery.Attempts != 1 || delivery.MessageStatus == "" {
			return nil
		}
		err = q.ApplyMessageStatus(ctx, delivery.MessageID, delivery.MessageStatus, delivery.ErrorCode, delivery.ErrorTitle)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	})
	return claimed, err
}

// FinishDelivery records the final outcome of a webhook.
func (q *Queries) FinishDelivery(ctx context.Context, id int64, state domain.DeliveryState, status int, lastError string, now time.Time) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE webhook_deliveries SET state = ?, last_status = ?, last_error = ?, finished_at = ? WHERE id = ?`,
		state, status, lastError, now.UnixMilli(), id)
	return err
}

// RetryDelivery puts a webhook back in the outbox for another attempt.
func (q *Queries) RetryDelivery(ctx context.Context, id int64, status int, lastError string, next time.Time) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE webhook_deliveries SET state = ?, last_status = ?, last_error = ?, next_attempt_at = ? WHERE id = ?`,
		domain.DeliveryPending, status, lastError, next.UnixMilli(), id)
	return err
}

// ReleaseInFlight requeues webhooks a previous run left in flight.
func (q *Queries) ReleaseInFlight(ctx context.Context, now time.Time) error {
	_, err := q.db.ExecContext(ctx, `UPDATE webhook_deliveries SET state = ?, next_attempt_at = ? WHERE state = ?`,
		domain.DeliveryPending, now.UnixMilli(), domain.DeliveryInFlight)
	return err
}

// GetDelivery returns one webhook.
func (q *Queries) GetDelivery(ctx context.Context, id int64) (domain.Delivery, error) {
	row := q.db.QueryRowContext(ctx, `SELECT `+deliveryColumns+` FROM webhook_deliveries WHERE id = ?`, id)
	delivery, err := scanDelivery(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Delivery{}, domain.ErrNotFound
	}
	return delivery, err
}

// ListDeliveries returns the most recent webhooks, newest first.
func (q *Queries) ListDeliveries(ctx context.Context, limit int) ([]domain.Delivery, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+deliveryColumns+` FROM webhook_deliveries ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	defer rows.Close()
	deliveries := []domain.Delivery{}
	for rows.Next() {
		delivery, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func scanDelivery(row scanner) (domain.Delivery, error) {
	var delivery domain.Delivery
	var nextAttempt, created int64
	var finished sql.NullInt64
	var badSignature int
	err := row.Scan(&delivery.ID, &delivery.Kind, &delivery.State, &delivery.Body, &delivery.Attempts, &nextAttempt,
		&delivery.LastStatus, &delivery.LastError, &delivery.MessageID, &delivery.MessageStatus,
		&delivery.ErrorCode, &delivery.ErrorTitle, &badSignature, &created, &finished)
	if err != nil {
		return domain.Delivery{}, err
	}
	delivery.NextAttemptAt = time.UnixMilli(nextAttempt).UTC()
	delivery.CreatedAt = time.UnixMilli(created).UTC()
	delivery.BadSignature = badSignature != 0
	delivery.FinishedAt = nullableTime(finished)
	return delivery, nil
}

// Console users.

// User is a console login. The password hash is opaque to the store.
type User struct {
	Email        string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ErrUserExists reports an email that already belongs to a user.
var ErrUserExists = errors.New("user already exists")

// InsertUser adds a user, failing with ErrUserExists if the email is taken.
func (q *Queries) InsertUser(ctx context.Context, user User) error {
	result, err := q.db.ExecContext(ctx, `
		INSERT INTO users (email, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (email) DO NOTHING`,
		user.Email, user.PasswordHash, user.CreatedAt.UnixMilli(), user.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrUserExists
	}
	return nil
}

// GetUser returns one user.
func (q *Queries) GetUser(ctx context.Context, email string) (User, error) {
	row := q.db.QueryRowContext(ctx, `SELECT email, password_hash, created_at, updated_at FROM users WHERE email = ?`, email)
	user, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, domain.ErrNotFound
	}
	return user, err
}

// ListUsers returns every user, by email.
func (q *Queries) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT email, password_hash, created_at, updated_at FROM users ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// CountUsers returns how many users exist.
func (q *Queries) CountUsers(ctx context.Context) (int, error) {
	var count int
	if err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}

// UpdateUserPassword replaces a user's password hash.
func (q *Queries) UpdateUserPassword(ctx context.Context, email, passwordHash string, now time.Time) error {
	result, err := q.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE email = ?`,
		passwordHash, now.UnixMilli(), email)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	return requireRow(result)
}

// DeleteUser removes a user.
func (q *Queries) DeleteUser(ctx context.Context, email string) error {
	result, err := q.db.ExecContext(ctx, `DELETE FROM users WHERE email = ?`, email)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return requireRow(result)
}

func scanUser(row scanner) (User, error) {
	var user User
	var created, updated int64
	if err := row.Scan(&user.Email, &user.PasswordHash, &created, &updated); err != nil {
		return User{}, err
	}
	user.CreatedAt = time.UnixMilli(created).UTC()
	user.UpdatedAt = time.UnixMilli(updated).UTC()
	return user, nil
}

// Reset removes conversations, media records, rules and webhooks. Templates and
// console users stay because they describe the setup rather than its activity.
// It returns the media file paths, for the caller to remove once the
// transaction commits.
func (q *Queries) Reset(ctx context.Context) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT path FROM media`)
	if err != nil {
		return nil, fmt.Errorf("list media paths: %w", err)
	}
	paths := []string{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			_ = rows.Close()
			return nil, err
		}
		paths = append(paths, path)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, table := range []string{"webhook_deliveries", "failure_rules", "messages", "media", "contacts"} {
		if _, err := q.db.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return nil, fmt.Errorf("clear %s: %w", table, err)
		}
	}
	return paths, nil
}

func requireRow(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	at := time.UnixMilli(value.Int64).UTC()
	return &at
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
