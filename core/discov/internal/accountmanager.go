package internal

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"sync"
	"time"
)

var (
	accounts      = make(map[string]Account)
	tlsConfigs    = make(map[string]*tls.Config)
	tokenPolicies = make(map[string]TokenPolicy)
	lock          sync.RWMutex
)

// Account holds the username/password for an etcd cluster.
type Account struct {
	User string
	Pass string
}

// TokenPolicy holds the JWT token TTL and the pre-refresh margin for an etcd cluster.
type TokenPolicy struct {
	TTL    time.Duration
	Margin time.Duration
}

// AddTokenPolicy registers the token TTL and refresh margin for the given etcd cluster.
// Zero or invalid values fall back to the built-in defaults, so callers can always
// invoke it with the raw config values.
func AddTokenPolicy(endpoints []string, ttl, margin time.Duration) {
	if ttl <= 0 {
		ttl = tokenTTL
	}
	if margin <= 0 {
		margin = tokenRefreshMargin
	}
	// margin 必须小于 ttl，否则预刷新间隔为负，退化为 ttl/2。
	if margin >= ttl {
		margin = ttl / 2
	}

	lock.Lock()
	defer lock.Unlock()
	tokenPolicies[getClusterKey(endpoints)] = TokenPolicy{
		TTL:    ttl,
		Margin: margin,
	}
}

// GetTokenPolicy returns the token policy for the given etcd cluster,
// falling back to the built-in defaults when not registered.
func GetTokenPolicy(endpoints []string) TokenPolicy {
	lock.RLock()
	defer lock.RUnlock()

	if policy, ok := tokenPolicies[getClusterKey(endpoints)]; ok {
		return policy
	}

	return TokenPolicy{
		TTL:    tokenTTL,
		Margin: tokenRefreshMargin,
	}
}

// AddAccount adds the username/password for the given etcd cluster.
func AddAccount(endpoints []string, user, pass string) {
	lock.Lock()
	defer lock.Unlock()

	accounts[getClusterKey(endpoints)] = Account{
		User: user,
		Pass: pass,
	}
}

// AddTLS adds the tls cert files for the given etcd cluster.
func AddTLS(endpoints []string, certFile, certKeyFile, caFile string, insecureSkipVerify bool) error {
	cert, err := tls.LoadX509KeyPair(certFile, certKeyFile)
	if err != nil {
		return err
	}

	caData, err := os.ReadFile(caFile)
	if err != nil {
		return err
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caData)

	lock.Lock()
	defer lock.Unlock()
	tlsConfigs[getClusterKey(endpoints)] = &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            pool,
		InsecureSkipVerify: insecureSkipVerify,
	}

	return nil
}

// GetAccount gets the username/password for the given etcd cluster.
func GetAccount(endpoints []string) (Account, bool) {
	lock.RLock()
	defer lock.RUnlock()

	account, ok := accounts[getClusterKey(endpoints)]
	return account, ok
}

// GetTLS gets the tls config for the given etcd cluster.
func GetTLS(endpoints []string) (*tls.Config, bool) {
	lock.RLock()
	defer lock.RUnlock()

	cfg, ok := tlsConfigs[getClusterKey(endpoints)]
	return cfg, ok
}
