package discov

import (
	"time"

	"github.com/shuguocloud/go-zero/core/discov/internal"
)

// RegisterAccount registers the username/password to the given etcd cluster.
func RegisterAccount(endpoints []string, user, pass string) {
	internal.AddAccount(endpoints, user, pass)
}

// RegisterTokenPolicy registers the JWT token TTL and pre-refresh margin to the
// given etcd cluster. Zero values fall back to the built-in defaults (24h / 30m).
func RegisterTokenPolicy(endpoints []string, ttl, margin time.Duration) {
	internal.AddTokenPolicy(endpoints, ttl, margin)
}

// RegisterTLS registers the CertFile/CertKeyFile/CACertFile to the given etcd.
func RegisterTLS(endpoints []string, certFile, certKeyFile, caFile string,
	insecureSkipVerify bool) error {
	return internal.AddTLS(endpoints, certFile, certKeyFile, caFile, insecureSkipVerify)
}
