package sim

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"strings"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/store"
)

//go:embed default_seed.json
var defaultSeed []byte

// Seed is the starting state of an account: its approved templates and the
// users available to simulate.
type Seed struct {
	Templates []cloudapi.TemplateDefinition `json:"templates"`
	Contacts  []SeedContact                 `json:"contacts"`
}

// SeedContact is a user that exists from the start.
type SeedContact struct {
	WaID string `json:"wa_id"`
	Name string `json:"name"`
}

// LoadSeed reads a seed file, or the built-in one when path is empty.
func LoadSeed(path string) (Seed, error) {
	content := defaultSeed
	if path != "" {
		var err error
		if content, err = os.ReadFile(path); err != nil {
			return Seed{}, fmt.Errorf("read seed file: %w", err)
		}
	}
	var seed Seed
	if err := json.Unmarshal(content, &seed); err != nil {
		return Seed{}, fmt.Errorf("parse seed: %w", err)
	}
	for index := range seed.Templates {
		if err := normalizeTemplate(&seed.Templates[index]); err != nil {
			return Seed{}, fmt.Errorf("seed template %d: %w", index, err)
		}
	}
	return seed, nil
}

// normalizeTemplate fills defaults. A seeded template is approved unless it says
// otherwise, because review only exists on the real platform.
func normalizeTemplate(template *cloudapi.TemplateDefinition) error {
	if template.Name == "" || template.Language == "" {
		return errors.New("name and language are required")
	}
	if template.Status == "" {
		template.Status = cloudapi.TemplateStatusApproved
	}
	if template.Category == "" {
		template.Category = "UTILITY"
	}
	template.Status = strings.ToUpper(template.Status)
	template.Category = strings.ToUpper(template.Category)
	template.ParameterFormat = template.Format()
	if template.ID == "" {
		template.ID = stableTemplateID(template.Name, template.Language)
	}
	return nil
}

// stableTemplateID keeps ids unchanged across restarts and machines, so
// fixtures that mention one keep working.
func stableTemplateID(name, language string) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(name + "/" + language))
	return fmt.Sprintf("%d", hash.Sum64()%9_000_000_000_000_000+1_000_000_000_000_000)
}

// ApplySeed loads the configured seed into the database. It is safe to run on
// every start: templates are upserted and existing contacts keep their state.
func (s *Service) ApplySeed(ctx context.Context) error {
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		for _, template := range s.config.Seed.Templates {
			if err := q.UpsertTemplate(ctx, template); err != nil {
				return err
			}
		}
		return s.seedContacts(ctx, q)
	})
	if err != nil {
		return fmt.Errorf("apply seed: %w", err)
	}
	return nil
}

func (s *Service) seedContacts(ctx context.Context, q *store.Queries) error {
	now := s.now()
	for _, contact := range s.config.Seed.Contacts {
		waID, apiErr := cloudapi.NormalizeRecipient(contact.WaID)
		if apiErr != nil {
			return fmt.Errorf("seed contact %q: %s", contact.WaID, apiErr.Definition.Details)
		}
		if _, err := s.ensureContact(ctx, q, waID, contact.Name, now); err != nil {
			return err
		}
	}
	return nil
}

// Reset clears all activity and restores the seeded contacts.
func (s *Service) Reset(ctx context.Context) error {
	var paths []string
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		var err error
		if paths, err = q.Reset(ctx); err != nil {
			return err
		}
		return s.seedContacts(ctx, q)
	})
	if err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.logger.Warn("remove media file", "path", path, "error", err)
		}
	}
	s.changed()
	return nil
}
