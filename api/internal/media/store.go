// Package media implements product/CMS image storage (change
// product-images-r2): presigned direct upload to an S3-compatible bucket
// (Cloudflare R2 in production, MinIO locally), server-side re-encoding to
// WebP in two sizes, and the sub-resource operations around them.
package media

import (
	"context"
	"errors"
	"time"
)

// ErrObjectNotFound is returned by ObjectStore.Get for a missing key.
var ErrObjectNotFound = errors.New("media: object not found")

// ErrObjectTooLarge is returned by ObjectStore.Get when the object exceeds the
// caller's byte limit (the object is not read to completion).
var ErrObjectTooLarge = errors.New("media: object exceeds size limit")

// ErrNotConfigured is returned by the disabled store; handlers map the
// service-level Enabled() check to 503 media_not_configured before it matters.
var ErrNotConfigured = errors.New("media: object storage is not configured")

// PresignedPut is what a browser needs to upload one object directly.
type PresignedPut struct {
	URL string
	// Headers MUST be sent verbatim with the PUT (they are part of the
	// signature). Content-Length is signed too, but browsers set it
	// themselves from the body, so it is not listed.
	Headers map[string]string
}

// ObjectStore is the whole storage surface (design D2). There is exactly one
// real implementation (S3Store); local development points it at MinIO instead
// of having a second, filesystem implementation, so the presign path - the
// easiest one to get wrong - is exercised locally.
type ObjectStore interface {
	PresignPut(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (PresignedPut, error)
	// Get reads an object, failing with ErrObjectTooLarge past maxBytes.
	Get(ctx context.Context, key string, maxBytes int64) ([]byte, error)
	Put(ctx context.Context, key string, body []byte, contentType, cacheControl string) error
	Delete(ctx context.Context, keys ...string) error
	// List returns every key with the given prefix.
	List(ctx context.Context, prefix string) ([]string, error)
}

// NullStore backs a disabled Service: every call fails with ErrNotConfigured.
// Keeping a non-nil store (instead of a nil branch everywhere) lets the wiring
// audit insist that no dependency is ever left unset.
type NullStore struct{}

func (NullStore) PresignPut(context.Context, string, string, int64, time.Duration) (PresignedPut, error) {
	return PresignedPut{}, ErrNotConfigured
}
func (NullStore) Get(context.Context, string, int64) ([]byte, error) { return nil, ErrNotConfigured }
func (NullStore) Put(context.Context, string, []byte, string, string) error {
	return ErrNotConfigured
}
func (NullStore) Delete(context.Context, ...string) error        { return ErrNotConfigured }
func (NullStore) List(context.Context, string) ([]string, error) { return nil, ErrNotConfigured }
