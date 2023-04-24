package view

import (
	"fmt"
	"github.com/eknkc/amber"
	"graecoFramework/util"
	template2 "html/template"
	"io"
	"path"
)

func NewAmberEngine(dir string, extension string) *AmberEngine {
	return &AmberEngine{viewDir: dir, ext: extension}
}

type AmberEngine struct {
	viewDir string
	ext     string
}

// templateName is a path to template inside `viewDir` directory
func (thiz *AmberEngine) Render(output io.Writer, templateName string, opts map[string]any) error {
	compiler := amber.New()

	tpl := template2.New(templateName)

	if opts == nil {
		opts = make(map[string]any)
	}
	opts = thiz.registerFunctions(opts)

	templatePath := path.Join(thiz.viewDir, fmt.Sprintf("%s.%s", templateName, thiz.ext))
	err := compiler.ParseFile(templatePath)
	if err != nil {
		return fmt.Errorf("Error during parse file %s, %v", templatePath, err)
	}

	tpl, err = compiler.CompileWithTemplate(tpl)
	if err != nil {
		return fmt.Errorf("Error during compile template %s, %v", templateName, err)
	}

	err = tpl.Execute(output, opts)
	if err != nil {
		return fmt.Errorf("Error during execute template %s, %v", templateName, err)
	}

	return nil
}

func (thiz *AmberEngine) registerFunctions(opts map[string]any) map[string]any {
	opts["fn"] = map[string]any{
		"InArray": util.InArray,
		"Pluck":   util.Pluck,
	}

	return opts
}
