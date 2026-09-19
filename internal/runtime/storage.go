package runtime

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// Storage ports inside the container: the S3 API and the web console.
const (
	StoragePort        = 9000
	StorageConsolePort = 9001
	// StorageRegion is what clients must send; local servers accept any region name.
	StorageRegion = "us-east-1"
	// StorageConsolePath is where RustFS serves its console.
	StorageConsolePath = "/rustfs/console/"
)

// StorageConfig is the per-project configuration of the object storage service.
type StorageConfig struct {
	// HostPort publishes the S3 API on the host (0 = not published); ConsolePort the
	// web console.
	HostPort    int `json:"hostPort"`
	ConsolePort int `json:"consolePort"`
	// AccessKey/SecretKey are the root credentials of this project's server.
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	// Bucket is created at start-up; PublicRead puts a policy on it that lets anyone read
	// every object – what public-read ACLs give on providers that honour them.
	Bucket     string `json:"bucket"`
	PublicRead bool   `json:"publicRead"`
}

// NewStorageConfig generates credentials for a fresh service.
func NewStorageConfig(bucket string) StorageConfig {
	return StorageConfig{AccessKey: "envoryx" + randomToken(12), SecretKey: randomToken(40), Bucket: bucket, PublicRead: true}
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))[:n]
}

// StorageBucketName derives a valid bucket name (3-63 lower-case letters, digits,
// hyphens) from a project slug.
func StorageBucketName(slug string) string {
	name := strings.ToLower(slug)
	if len(name) < 3 {
		name += "-bucket"
	}
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

// ContainerEnv returns the variables the storage container itself needs.
func (c StorageConfig) ContainerEnv() []string {
	return []string{"RUSTFS_ACCESS_KEY=" + c.AccessKey, "RUSTFS_SECRET_KEY=" + c.SecretKey, "RUSTFS_CONSOLE_ENABLE=true", "RUSTFS_VOLUMES=/data"}
}

// StorageEnvKeys is the order in which the injected variables appear.
var StorageEnvKeys = []string{
	"S3_ENDPOINT", "S3_REGION", "S3_BUCKET", "S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_USE_PATH_STYLE", "S3_PUBLIC_ENDPOINT", "S3_PUBLIC_URL",
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_DEFAULT_REGION", "AWS_BUCKET", "AWS_ENDPOINT", "AWS_USE_PATH_STYLE_ENDPOINT", "AWS_URL",
}

// StorageEnv returns the variables injected into application containers: a generic S3_*
// set and the AWS_* names Laravel's s3 disk and the AWS SDKs read. publicURL is the
// browser-reachable URL of the bucket (for Storage::url() and direct uploads); the
// public endpoint (its scheme and host) is what presigned URLs meant for a browser must
// be signed with, since the signature covers the host.
func StorageEnv(c StorageConfig, publicURL string) map[string]string {
	endpoint := "http://s3:9000"
	publicEndpoint := strings.TrimSuffix(publicURL, "/"+c.Bucket)
	return map[string]string{
		"S3_ENDPOINT": endpoint, "S3_REGION": StorageRegion, "S3_BUCKET": c.Bucket, "S3_ACCESS_KEY": c.AccessKey, "S3_SECRET_KEY": c.SecretKey, "S3_USE_PATH_STYLE": "true", "S3_PUBLIC_ENDPOINT": publicEndpoint, "S3_PUBLIC_URL": publicURL,
		"AWS_ACCESS_KEY_ID": c.AccessKey, "AWS_SECRET_ACCESS_KEY": c.SecretKey, "AWS_DEFAULT_REGION": StorageRegion, "AWS_BUCKET": c.Bucket, "AWS_ENDPOINT": endpoint, "AWS_USE_PATH_STYLE_ENDPOINT": "true", "AWS_URL": publicURL,
	}
}
