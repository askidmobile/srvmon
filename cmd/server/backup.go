package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/backup"
	"github.com/askidmobile/srvmon/internal/config"
	"github.com/askidmobile/srvmon/internal/secret"
)

// masterKey prints the master key (base64) or, with -import, replaces it
// with one read from stdin.
//
//	srvmon-server master-key            print the key
//	srvmon-server master-key -import    read a base64 key from stdin
func masterKey(args []string) error {
	fs := flag.NewFlagSet("master-key", flag.ContinueOnError)
	imp := fs.Bool("import", false, "replace the master key with a base64 key read from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	if !*imp {
		key, err := secret.ReadKeyFile(cfg.MasterKeyFile)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Master key of this panel. Keep it offline (password manager); backups cannot be read without it:")
		fmt.Println(base64.StdEncoding.EncodeToString(key))
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != 32 {
		return errors.New("stdin must hold the base64 master key (32 bytes)")
	}
	if old, err := os.ReadFile(cfg.MasterKeyFile); err == nil {
		if bytes.Equal(old, key) {
			fmt.Println("the master key is already this one")
			return nil
		}
		keep := cfg.MasterKeyFile + ".old-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.WriteFile(keep, old, 0o600); err != nil {
			return err
		}
		fmt.Println("previous key kept as", keep)
	}
	if err := secret.WriteKeyFile(cfg.MasterKeyFile, key); err != nil {
		return err
	}
	fmt.Println("master key written to", cfg.MasterKeyFile)
	return nil
}

// restore replaces the database with a backup. srvmon-server must be
// stopped. The backup comes from a file (- for stdin) or from S3 (the
// newest one unless -object is given; the secret key is read from
// SRVMON_S3_SECRET_KEY).
func restore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	file := fs.String("file", "", "backup file, - for stdin")
	var s3 backup.S3
	fs.StringVar(&s3.Endpoint, "s3-endpoint", "", "S3 endpoint, e.g. storage.yandexcloud.net")
	fs.StringVar(&s3.Region, "s3-region", "", "S3 region")
	fs.StringVar(&s3.Bucket, "s3-bucket", "", "S3 bucket")
	fs.StringVar(&s3.Prefix, "s3-prefix", "", "key prefix, e.g. srvmon/")
	fs.StringVar(&s3.AccessKey, "s3-access-key", "", "S3 access key id (secret: SRVMON_S3_SECRET_KEY)")
	fs.BoolVar(&s3.PathStyle, "s3-path-style", false, "path-style bucket addressing")
	object := fs.String("object", "", "object key in the bucket (default: the newest backup)")
	list := fs.Bool("list", false, "only list the backups in the bucket")
	force := fs.Bool("force", false, "replace an existing database (it is kept as a copy)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	var data []byte
	switch {
	case *file == "-":
		data, err = io.ReadAll(os.Stdin)
	case *file != "":
		data, err = os.ReadFile(*file)
	case s3.Endpoint != "":
		data, err = fetchS3(ctx, s3, *object, *list)
		if *list {
			return err
		}
	default:
		return errors.New("give -file PATH (or - for stdin) or the -s3-* flags")
	}
	if err != nil {
		return err
	}
	key, err := secret.ReadKeyFile(cfg.MasterKeyFile)
	if err != nil {
		return fmt.Errorf("%w (import the saved key first: srvmon-server master-key -import)", err)
	}
	box, err := secret.New(key)
	if err != nil {
		return err
	}
	m, db, err := backup.Open(box, data)
	if err != nil {
		return err
	}
	fmt.Printf("backup of %s, srvmon %s, schema %d, database %d bytes\n",
		m.CreatedAt.Format(time.RFC3339), m.Version, m.Schema, len(db))
	kept, err := backup.RestoreDB(cfg.DBPath(), db, *force, time.Now())
	if kept != "" {
		fmt.Println("previous database kept as", kept)
	}
	if err != nil {
		return err
	}
	fmt.Println("restored to", cfg.DBPath(), "- start srvmon-server now")
	return nil
}

func fetchS3(ctx context.Context, c backup.S3, object string, list bool) ([]byte, error) {
	if c.Prefix != "" && !strings.HasSuffix(c.Prefix, "/") {
		c.Prefix += "/"
	}
	if field, ok := c.Validate(); !ok {
		return nil, fmt.Errorf("check -s3-%s", strings.ReplaceAll(field, "_", "-"))
	}
	sk := os.Getenv("SRVMON_S3_SECRET_KEY")
	if sk == "" {
		return nil, errors.New("set SRVMON_S3_SECRET_KEY")
	}
	t, err := backup.NewS3(c, sk)
	if err != nil {
		return nil, err
	}
	if list || object == "" {
		objs, err := backup.Backups(ctx, t, c.Prefix)
		if err != nil {
			return nil, err
		}
		if list {
			for _, o := range objs {
				fmt.Printf("%s  %8d  %s\n", o.At.Format(time.RFC3339), o.Size, o.Key)
			}
			return nil, nil
		}
		if len(objs) == 0 {
			return nil, errors.New("no backups under this prefix")
		}
		object = objs[0].Key
	}
	fmt.Println("downloading", object)
	return t.Get(ctx, object)
}
