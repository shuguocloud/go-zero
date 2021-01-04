package template

var (
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
