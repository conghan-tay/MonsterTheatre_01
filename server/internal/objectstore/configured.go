package objectstore

import (
	"context"
	"fmt"

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
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("load AWS configuration: %w", err)
		}
		return NewS3(s3.NewFromConfig(awsCfg), cfg.S3Bucket), "s3", nil
	}
	store, err := NewFS(cfg.ObjectStoreDir)
	return store, "filesystem", err
}
