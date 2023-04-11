package view

import (
	"fmt"
	"github.com/eknkc/amber"
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
func (thiz *AmberEngine) Render(output io.Writer, templateName string, opts any) error {
	compiler := amber.New()

	templatePath := path.Join(thiz.viewDir, fmt.Sprintf("%s.%s", templateName, thiz.ext))
	err := compiler.ParseFile(templatePath)
	if err != nil {
		return fmt.Errorf("Error during parse file %s, %v", templatePath, err)
	}

	tpl, err := compiler.Compile()
	if err != nil {
		return fmt.Errorf("Error during compile template %s, %v", templateName, err)
	}
	err = tpl.Execute(output, opts)
	if err != nil {
		return fmt.Errorf("Error during execute template %s, %v", templateName, err)
	}

	return nil
}
