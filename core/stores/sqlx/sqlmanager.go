package sqlx

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/shuguocloud/go-zero/core/logx"
	"github.com/shuguocloud/go-zero/core/syncx"
)

const (
	// defaultMaxIdleConns is the default max idle connections of the db pool.
	defaultMaxIdleConns = 64
	// defaultMaxOpenConns is the default max open connections of the db pool.
	defaultMaxOpenConns = 64
	// defaultMaxLifetime is the default max lifetime of db connections.
	defaultMaxLifetime = time.Minute
	// pingTimeout is the max duration of the startup ping, Audit 027.
	pingTimeout = 5 * time.Second
)

// Audit 015：池参数由 const 硬编码改为可通过 SetPoolOptions 覆盖的默认值
var (
	maxIdleConns = defaultMaxIdleConns
	maxOpenConns = defaultMaxOpenConns
	maxLifetime  = defaultMaxLifetime
	maxIdleTime  time.Duration
)

// SetPoolOptions overrides the db pool settings for connections created after this call.
// Non-positive values are ignored (keep current).
// NOTE: connections are cached per DSN, so call this before the first use.
func SetPoolOptions(maxIdle, maxOpen int, lifetime, idleTime time.Duration) {
	if maxIdle > 0 {
		maxIdleConns = maxIdle
	}
	if maxOpen > 0 {
		maxOpenConns = maxOpen
	}
	if lifetime > 0 {
		maxLifetime = lifetime
	}
	if idleTime > 0 {
		maxIdleTime = idleTime
	}
}

var connManager = syncx.NewResourceManager()

func getCachedSqlConn(driverName, server string) (*sql.DB, error) {
	val, err := connManager.GetResource(server, func() (io.Closer, error) {
		conn, err := newDBConnection(driverName, server)
		if err != nil {
			return nil, err
		}

		if driverName == mysqlDriverName {
			if cfg, e := mysql.ParseDSN(server); e != nil {
				// if cannot parse, don't collect the metrics
				logx.Error(e)
			} else {
				checksum := sha256.Sum256([]byte(server))
				connCollector.registerClient(&statGetter{
					host:   cfg.Addr,
					dbName: cfg.DBName,
					hash:   hex.EncodeToString(checksum[:]),
					poolStats: func() sql.DBStats {
						return conn.Stats()
					},
				})
			}
		}

		return conn, nil
	})
	if err != nil {
		return nil, err
	}

	return val.(*sql.DB), nil
}

func getSqlConn(driverName, server string) (*sql.DB, error) {
	conn, err := getCachedSqlConn(driverName, server)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

func newDBConnection(driverName, datasource string) (*sql.DB, error) {
	conn, err := sql.Open(driverName, datasource)
	if err != nil {
		return nil, err
	}

	// we need to do this until the issue https://github.com/golang/go/issues/9851 get fixed
	// discussed here https://github.com/go-sql-driver/mysql/issues/257
	// if the discussed SetMaxIdleTimeout methods added, we'll change this behavior
	// 8 means we can't have more than 8 goroutines to concurrently access the same database.
	conn.SetMaxIdleConns(maxIdleConns)
	conn.SetMaxOpenConns(maxOpenConns)
	conn.SetConnMaxLifetime(maxLifetime)
	if maxIdleTime > 0 {
		conn.SetConnMaxIdleTime(maxIdleTime)
	}

	// Audit 027：启动探活带超时，DB 不可达时快速失败并输出明确原因，避免启动长时间卡死
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()

	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return conn, nil
}
