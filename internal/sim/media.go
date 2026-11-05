package sim

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/domain"
)

// AttachmentsPath is where minted media URLs point, the same path as Meta's
// lookaside host.
const AttachmentsPath = "/whatsapp_business/attachments/"

// mediaURLLifetime is documented by Meta: the URL dies after 5 minutes and the
// consumer asks for a new one.
const mediaURLLifetime = 5 * time.Minute

// SaveMedia stores a file a simulated user is about to send. It enforces the
// platform's type and size limits, which a real user could never get past.
func (s *Service) SaveMedia(ctx context.Context, content io.Reader, filename, mimeType string) (domain.Media, error) {
	rule, err := cloudapi.LookupMediaRule(mimeType)
	if err != nil {
		return domain.Media{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := os.MkdirAll(s.config.MediaDir, 0o750); err != nil {
		return domain.Media{}, fmt.Errorf("create media directory: %w", err)
	}
	id := newMediaID()
	path := filepath.Join(s.config.MediaDir, id)
	size, digest, err := writeMediaFile(path, content, rule.MaxBytes)
	if err != nil {
		_ = os.Remove(path)
		return domain.Media{}, err
	}
	media := domain.Media{
		ID:        id,
		MimeType:  mimeType,
		SHA256:    digest,
		FileSize:  size,
		Filename:  filepath.Base(filename),
		Path:      path,
		CreatedAt: s.now(),
	}
	if err := s.store.InsertMedia(ctx, media); err != nil {
		_ = os.Remove(path)
		return domain.Media{}, err
	}
	return media, nil
}

func writeMediaFile(path string, content io.Reader, maxBytes int64) (int64, string, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return 0, "", fmt.Errorf("create media file: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(content, maxBytes+1))
	if err != nil {
		return 0, "", fmt.Errorf("write media file: %w", err)
	}
	if size > maxBytes {
		return 0, "", fmt.Errorf("%w: file is larger than the %d bytes allowed for this type", ErrInvalid, maxBytes)
	}
	if size == 0 {
		return 0, "", fmt.Errorf("%w: file is empty", ErrInvalid)
	}
	return size, base64.StdEncoding.EncodeToString(hash.Sum(nil)), nil
}

// GetMedia returns a media record.
func (s *Service) GetMedia(ctx context.Context, id string) (domain.Media, error) {
	return s.store.GetMedia(ctx, id)
}

// ResolvedMedia is a media record plus a freshly minted download URL.
type ResolvedMedia struct {
	domain.Media
	URL string
}

// ResolveMedia answers GET /{version}/{media-id}.
func (s *Service) ResolveMedia(ctx context.Context, id string) (ResolvedMedia, error) {
	media, err := s.store.GetMedia(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return ResolvedMedia{}, cloudapi.NewUnknownObjectError(id)
	}
	if err != nil {
		return ResolvedMedia{}, err
	}
	return ResolvedMedia{Media: media, URL: s.mediaURL(id)}, nil
}

// OpenAttachment validates a minted URL's query and returns its media.
func (s *Service) OpenAttachment(ctx context.Context, query url.Values) (domain.Media, error) {
	id, expiry, hash := query.Get("mid"), query.Get("ext"), query.Get("hash")
	expiresAt, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil {
		return domain.Media{}, domain.ErrNotFound
	}
	if !hmac.Equal([]byte(hash), []byte(s.mediaURLHash(id, expiry))) || s.now().Unix() > expiresAt {
		return domain.Media{}, domain.ErrNotFound
	}
	return s.store.GetMedia(ctx, id)
}

func (s *Service) mediaURL(id string) string {
	expiry := strconv.FormatInt(s.now().Add(mediaURLLifetime).Unix(), 10)
	query := url.Values{
		"mid":  {id},
		"ext":  {expiry},
		"hash": {s.mediaURLHash(id, expiry)},
	}
	return s.config.PublicURL + AttachmentsPath + "?" + query.Encode()
}

func (s *Service) mediaURLHash(id, expiry string) string {
	mac := hmac.New(sha256.New, s.urlKey)
	mac.Write([]byte(id + "." + expiry))
	return hex.EncodeToString(mac.Sum(nil))
}

func newMediaID() string {
	value, err := rand.Int(rand.Reader, big.NewInt(9_000_000_000_000_000))
	if err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return value.Add(value, big.NewInt(1_000_000_000_000_000)).String()
}
