package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/awslabs/aurora-dsql-connectors/go/pgx/dsql"
	"github.com/jackc/pgx/v5"
)

// Environment variables that point the suite at a deployment instead of a
// stack of its own. All three are needed together.
const (
	// EnvBaseURL is the deployment's public address, exactly as a browser
	// shows it, for example "https://d111111abcdef8.cloudfront.net".
	EnvBaseURL = "E2E_BASE_URL"
	// EnvDSQLEndpoint is the Aurora DSQL cluster's endpoint.
	EnvDSQLEndpoint = "E2E_DSQL_ENDPOINT"
	// EnvS3Bucket is the bucket portraits and recordings are kept in.
	EnvS3Bucket = "E2E_S3_BUCKET"
)

// connectRemote returns a Stack that is a deployment somebody else started.
// Nothing is started here and nothing is stopped at the end.
//
// The tests still create directors and read rows and stored objects from
// outside, as they do locally. Against a deployment they do it from this
// machine, with whatever AWS identity it is signed in as: as the admin
// database role on the cluster, and with read access to the bucket.
//
// The deployment must have been started with the fake providers and the
// timings this suite expects (VOTE_WINDOW=2s, PREMIERE_COUNTDOWN=1s,
// CHOICES_RETRY_WINDOW=4s). Tests that need more than that (a second
// server, a worker to kill, scripts on the worker's disk) skip themselves.
func connectRemote(ctx context.Context) (stack *Stack, err error) {
	baseURL := strings.TrimRight(os.Getenv(EnvBaseURL), "/")
	endpoint := os.Getenv(EnvDSQLEndpoint)
	bucket := os.Getenv(EnvS3Bucket)
	if endpoint == "" || bucket == "" {
		return nil, fmt.Errorf("%s is set, so %s and %s must be set too", EnvBaseURL, EnvDSQLEndpoint, EnvS3Bucket)
	}
	region, err := dsql.ParseRegion(endpoint)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvDSQLEndpoint, err)
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		return nil, err
	}
	workDir, err := os.MkdirTemp("", "monstertheatre-e2e-remote-")
	if err != nil {
		return nil, err
	}
	s := &Stack{
		BaseURL:       baseURL,
		PublicBaseURL: baseURL,
		Remote:        true,
		LogDir:        filepath.Join(workDir, "logs"),
		repoRoot:      repoRoot,
		workDir:       workDir,
		dsqlEndpoint:  endpoint,
		bucket:        bucket,
		// What adddirector needs to reach the cluster.
		env: childEnv(map[string]string{"DSQL_ENDPOINT": endpoint, "DSQL_USER": "admin"}),
	}
	defer func() {
		if err != nil {
			s.Stop()
		}
	}()
	if err := os.MkdirAll(filepath.Join(workDir, "bin"), 0o755); err != nil {
		return nil, err
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	s.s3 = s3.NewFromConfig(awsCfg)

	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(workDir, "bin")+string(os.PathSeparator), "./cmd/adddirector")
	build.Dir = filepath.Join(repoRoot, "server")
	if out, err := build.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build adddirector: %w\n%s", err, out)
	}

	// A request only the Go server answers this way, so a deployment whose
	// routing is wrong fails here and not in the first test.
	resp, err := http.Get(baseURL + "/api/v1/director/me")
	if err != nil {
		return nil, fmt.Errorf("reach the deployment: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), `"error"`) {
		return nil, fmt.Errorf("%s/api/v1/director/me answered %d %q; expected the server's 401", baseURL, resp.StatusCode, body)
	}
	// And the cluster, so a missing sign-in fails here too.
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to Aurora DSQL as admin: %w", err)
	}
	conn.Close(ctx)
	return s, nil
}

// connect opens a connection to the database the stack uses: the local test
// database, or the deployment's cluster as admin.
func (s *Stack) connect(ctx context.Context) (*pgx.Conn, error) {
	if s.Remote {
		return dsql.Connect(ctx, dsql.Config{Host: s.dsqlEndpoint})
	}
	return pgx.Connect(ctx, e2eDatabaseURL)
}

// storedObjectS3 reads an object from the deployment's bucket. A missing
// one is reported as os.ErrNotExist, as the local store reports it.
func (s *Stack) storedObjectS3(key string) ([]byte, error) {
	out, err := s.s3.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		var missing interface{ ErrorCode() string }
		if errors.As(err, &missing) && missing.ErrorCode() == "NoSuchKey" {
			return nil, fmt.Errorf("%s: %w", key, os.ErrNotExist)
		}
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}
