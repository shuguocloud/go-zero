package gen

import (
	"github.com/shuguocloud/go-zero/tools/goctl/model/sql/template"
	"github.com/shuguocloud/go-zero/tools/goctl/util"
	"github.com/shuguocloud/go-zero/tools/goctl/util/pathx"
)

func genTableName(table Table) (string, error) {
	text, err := pathx.LoadTemplate(category, tableNameTemplateFile, template.TableName)
	if err != nil {
		return "", err
	}

	output, err := util.With("tableName").
		Parse(text).
		Execute(map[string]any{
			"tableName":             table.Name.Source(),
			"upperStartCamelObject": table.Name.ToCamel(),
		})
	if err != nil {
		return "", nil
	}

	return output.String(), nil
}
