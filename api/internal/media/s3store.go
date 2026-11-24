package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"ksdevworks/ecommerce/api/internal/config"
)

// S3Store implements ObjectStore for Cloudflare R2 and MinIO.
type S3Store struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

// NewS3Store builds a store from validated media config. No network call is
// made here.
func NewS3Store(cfg config.MediaConfig) *S3Store {
	client := s3.New(s3.Options{
		// R2 ignores the region but SigV4 needs one; "auto" is what R2 documents.
		Region:       "auto",
		BaseEndpoint: aws.String(cfg.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		// Path-style works on R2 and MinIO alike (MinIO needs it for 127.0.0.1).
		UsePathStyle: true,
		// Newer SDKs add a CRC32 checksum header/param by default; presigned PUTs
		// from a browser then fail on R2. Only compute checksums when required.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &S3Store{client: client, presign: s3.NewPresignClient(client), bucket: cfg.Bucket}
}

func (s *S3Store) PresignPut(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (PresignedPut, error) {
	req, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return PresignedPut{}, fmt.Errorf("media: presign put: %w", err)
	}
	return PresignedPut{URL: req.URL, Headers: map[string]string{"Content-Type": contentType}}, nil
}

func (s *S3Store) Get(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		var nsk *types.NoSuchKey
		var apiErr smithy.APIError
		if errors.As(err, &nsk) || (errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound")) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("media: get %q: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()
	if out.ContentLength != nil && *out.ContentLength > maxBytes {
		return nil, ErrObjectTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(out.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("media: read %q: %w", key, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrObjectTooLarge
	}
	return data, nil
}

func (s *S3Store) Put(ctx context.Context, key string, body []byte, contentType, cacheControl string) error {
	in := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	}
	if cacheControl != "" {
		in.CacheControl = aws.String(cacheControl)
	}
	if _, err := s.client.PutObject(ctx, in); err != nil {
		return fmt.Errorf("media: put %q: %w", key, err)
	}
	return nil
}

func (s *S3Store) Delete(ctx context.Context, keys ...string) error {
	for len(keys) > 0 {
		n := len(keys)
		if n > 1000 {
			n = 1000
		}
		ids := make([]types.ObjectIdentifier, 0, n)
		for _, k := range keys[:n] {
			ids = append(ids, types.ObjectIdentifier{Key: aws.String(k)})
		}
		out, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("media: delete: %w", err)
		}
		if len(out.Errors) > 0 {
			return fmt.Errorf("media: delete: %d object(s) failed, first %q: %s",
				len(out.Errors), aws.ToString(out.Errors[0].Key), aws.ToString(out.Errors[0].Message))
		}
		keys = keys[n:]
	}
	return nil
}

func (s *S3Store) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("media: list %q: %w", prefix, err)
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	return keys, nil
}
