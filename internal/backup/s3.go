package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 is where backups go: any S3-compatible storage (AWS, Yandex Object
// Storage, Cloudflare R2, Backblaze B2, MinIO, ...).
type S3 struct {
	// Endpoint is host[:port] or a URL; http:// is allowed for a storage
	// in a private network, otherwise TLS is used.
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"` // e.g. "srvmon/"
	AccessKey string `json:"access_key"`
	PathStyle bool   `json:"path_style"`
}

var (
	bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	prefixRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{0,200}$`)
	regionRe = regexp.MustCompile(`^[a-z0-9-]{0,40}$`)
)

// Validate checks the fields; it returns the name of the bad field.
func (c S3) Validate() (field string, ok bool) {
	if _, _, err := c.endpoint(); err != nil {
		return "endpoint", false
	}
	if !bucketRe.MatchString(c.Bucket) {
		return "bucket", false
	}
	if !prefixRe.MatchString(c.Prefix) || strings.HasPrefix(c.Prefix, "/") || strings.Contains(c.Prefix, "..") {
		return "prefix", false
	}
	if !regionRe.MatchString(c.Region) {
		return "region", false
	}
	if c.AccessKey == "" || len(c.AccessKey) > 256 || strings.ContainsAny(c.AccessKey, " \t\r\n") {
		return "access_key", false
	}
	return "", true
}

func (c S3) endpoint() (host string, secure bool, err error) {
	e := strings.TrimSpace(c.Endpoint)
	secure = true
	if strings.Contains(e, "://") {
		u, err := url.Parse(e)
		if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.User != nil || u.RawQuery != "" {
			return "", false, errors.New("endpoint must be host[:port] or https://host[:port]")
		}
		switch u.Scheme {
		case "https":
		case "http":
			secure = false
		default:
			return "", false, errors.New("endpoint scheme must be https or http")
		}
		e = u.Host
	}
	if e == "" || strings.ContainsAny(e, "/ \t") {
		return "", false, errors.New("endpoint must be host[:port]")
	}
	return e, secure, nil
}

// Object is one backup in the bucket.
type Object struct {
	Key  string    `json:"key"`
	Size int64     `json:"size"`
	At   time.Time `json:"at"`
}

// Target stores backups.
type Target interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	List(ctx context.Context, prefix string) ([]Object, error)
	Delete(ctx context.Context, key string) error
}

type s3Target struct {
	c      *minio.Client
	bucket string
}

// NewS3 connects to the storage with the access key and its secret.
func NewS3(c S3, secretKey string) (Target, error) {
	host, secure, err := c.endpoint()
	if err != nil {
		return nil, err
	}
	lookup := minio.BucketLookupAuto
	if c.PathStyle {
		lookup = minio.BucketLookupPath
	}
	cl, err := minio.New(host, &minio.Options{
		Creds:        credentials.NewStaticV4(c.AccessKey, secretKey, ""),
		Secure:       secure,
		Region:       c.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, err
	}
	return &s3Target{c: cl, bucket: c.Bucket}, nil
}

func (t *s3Target) Put(ctx context.Context, key string, data []byte) error {
	_, err := t.c.PutObject(ctx, t.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: len(data) < 64<<20})
	return s3Err(err)
}

func (t *s3Target) Get(ctx context.Context, key string) ([]byte, error) {
	o, err := t.c.GetObject(ctx, t.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, s3Err(err)
	}
	defer o.Close()
	b, err := io.ReadAll(io.LimitReader(o, maxDB+1<<20))
	return b, s3Err(err)
}

func (t *s3Target) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	for o := range t.c.ListObjects(ctx, t.bucket, minio.ListObjectsOptions{Prefix: prefix}) {
		if o.Err != nil {
			return nil, s3Err(o.Err)
		}
		out = append(out, Object{Key: o.Key, Size: o.Size, At: o.LastModified.UTC()})
	}
	return out, nil
}

func (t *s3Target) Delete(ctx context.Context, key string) error {
	return s3Err(t.c.RemoveObject(ctx, t.bucket, key, minio.RemoveObjectOptions{}))
}

// s3Err shortens S3 errors to what the owner can act on.
func s3Err(err error) error {
	if err == nil {
		return nil
	}
	var r minio.ErrorResponse
	if errors.As(err, &r) && r.Code != "" {
		if r.Message != "" {
			return fmt.Errorf("S3 %s: %s", r.Code, r.Message)
		}
		return fmt.Errorf("S3 %s", r.Code)
	}
	return err
}

// ObjectName is the key of a backup made at t.
func ObjectName(prefix string, t time.Time) string {
	return prefix + "srvmon-" + t.UTC().Format("20060102T150405Z") + Ext
}

// Backups lists the backups under prefix, newest first. Other objects in
// the bucket are ignored.
func Backups(ctx context.Context, t Target, prefix string) ([]Object, error) {
	all, err := t.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	var out []Object
	for _, o := range all {
		name := strings.TrimPrefix(o.Key, prefix)
		if strings.HasPrefix(name, "srvmon-") && strings.HasSuffix(name, Ext) && !strings.Contains(name, "/") {
			out = append(out, o)
		}
	}
	// Names carry the UTC time, so they sort by age.
	sort.Slice(out, func(i, j int) bool { return out[i].Key > out[j].Key })
	return out, nil
}
