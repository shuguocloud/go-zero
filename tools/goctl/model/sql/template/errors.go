package template

var Error = `package {{.pkg}}

import "github.com/shuguocloud/go-zero/core/stores/sqlx"

var ErrNotFound = sqlx.ErrNotFound
`
