package view

import (
	"fmt"
	"gorgany/app/core"
	template2 "html/template"
	"io"
	"os"
	"path"
	"regexp"
)

func NewNativeEngine(viewDir, ext string) core.IViewEngine {
	return &NativeEngine{
		viewDir: viewDir,
		ext:     ext,
	}
}

type NativeEngine struct {
	viewDir string
	ext     string
}

func (thiz NativeEngine) Render(output io.Writer, templateName string, opts map[string]any) error {
	templatePath := path.Join(thiz.viewDir, fmt.Sprintf("%s.%s", templateName, thiz.ext))

	content, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("Error during read file %s, %v", templatePath, err)
	}

	processedContent, imports := thiz.processImports(string(content))

	funcs, ok := opts["fn"]
	if !ok {
		funcs = make(map[string]any)
	}

	tpl := template2.New(templatePath).Funcs(funcs.(map[string]any))

	for _, imp := range imports {
		tpl, err = tpl.Parse(imp)
		if err != nil {
			return fmt.Errorf("Error during parse file %s, %v", templatePath, err)
		}
	}

	tpl, err = tpl.Parse(processedContent)
	if err != nil {
		return fmt.Errorf("Error during parse file %s, %v", templatePath, err)
	}

	err = tpl.Execute(output, opts)
	if err != nil {
		return fmt.Errorf("Error during execute template %s, %v", templateName, err)
	}

	return nil
}

func (thiz NativeEngine) processImports(templateContent string) (string, []string) {
	imports := make([]string, 0)
	regex := regexp.MustCompile(`{{ *import (?P<templateName>.+?) *}}`)
	processedContent := regex.ReplaceAllStringFunc(templateContent, func(defineImport string) string {
		_import := regex.FindStringSubmatch(defineImport)
		if len(_import) < 2 {
			return ""
		}
		path := path.Join(thiz.viewDir, fmt.Sprintf("%s.%s", _import[1], thiz.ext))
		content, err := os.ReadFile(path)
		if err != nil {
			fmt.Println(err)
			return ""
		}
		imports = append(imports, string(content))
		return ""
	})

	return processedContent, imports
}
