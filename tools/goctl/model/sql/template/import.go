package template

var (
	// Imports defines a import template for model in cache case
	Imports = `import (
	"database/sql"
	"fmt"
	"strings"
	{{if .time}}"time"{{end}}

	"github.com/shuguocloud/go-zero/core/stores/cache"
	"github.com/shuguocloud/go-zero/core/stores/sqlc"
	"github.com/shuguocloud/go-zero/core/stores/sqlx"
	"github.com/shuguocloud/go-zero/core/stringx"
	"github.com/shuguocloud/go-zero/tools/goctl/model/sql/builderx"
)
`
	// ImportsNoCache defines a import template for model in normal case
	ImportsNoCache = `import (
	"database/sql"
	"fmt"
	"strings"
	{{if .time}}"time"{{end}}

	"github.com/shuguocloud/go-zero/core/stores/sqlc"
	"github.com/shuguocloud/go-zero/core/stores/sqlx"
	"github.com/shuguocloud/go-zero/core/stringx"
	"github.com/shuguocloud/go-zero/tools/goctl/model/sql/builderx"
)
`
)
