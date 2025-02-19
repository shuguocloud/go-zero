package dm

import (
    // imports the driver.
    _ "gitee.com/chunanyong/dm"
    "github.com/shuguocloud/go-zero/core/stores/sqlx"
)

const dmDriverName = "dm"

// New returns a clickhouse connection.
func New(datasource string, opts ...sqlx.SqlOption) sqlx.SqlConn {
    return sqlx.NewSqlConn(dmDriverName, datasource, opts...)
}