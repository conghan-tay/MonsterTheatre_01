package objectstore

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
)

// Configured builds the store the configuration asks for: S3 when a bucket
// is set, and otherwise the filesystem. It also says which, for the startup
// log. The S3 client takes its region and credentials from the process's
// AWS environment.
func Configured(ctx context.Context, cfg config.Config) (Store, string, error) {
	if cfg.S3Bucket != "" {
		client, err := newS3Client(ctx)
		if err != nil {
			return nil, "", err
		}
		return NewS3(client, cfg.S3Bucket), "s3", nil
	}
	store, err := NewFS(cfg.ObjectStoreDir)
	return store, "filesystem", err
}

// newS3Client builds the client from the process's AWS environment.
//
// The SDK's default is to check a checksum on every response and to log a
// line when S3 sends none, which it does not for part of an object. Every
// ranged read would log that line, so checksums are checked only where S3
// requires them.
func newS3Client(ctx context.Context) (*s3.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return s3.NewFromConfig(awsCfg), nil
}
