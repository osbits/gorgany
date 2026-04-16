package view

import (
	"fmt"
	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/log"
	template2 "html/template"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
)

const InternalTemplatePath = "../resource/template"

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
		if os.IsNotExist(err) {
			_, callerFilename, _, _ := runtime.Caller(0)
			dir := filepath.Dir(callerFilename)
			templatePath = path.Join(dir, InternalTemplatePath, fmt.Sprintf("%s.%s", templateName, thiz.ext))
			content, err = os.ReadFile(templatePath)
			if err != nil {
				return fmt.Errorf("Error during read file %s, %v", templatePath, err)
			}
		} else {
			return fmt.Errorf("Error during read file %s, %v", templatePath, err)
		}
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

		templatePath := path.Join(thiz.viewDir, fmt.Sprintf("%s.%s", _import[1], thiz.ext))
		content, err := os.ReadFile(templatePath)
		if err != nil {
			if os.IsNotExist(err) {
				_, callerFilename, _, _ := runtime.Caller(0)
				dir := filepath.Dir(callerFilename)
				templatePath = path.Join(dir, InternalTemplatePath, fmt.Sprintf("%s.%s", _import[1], thiz.ext))
				content, err = os.ReadFile(templatePath)
				if err != nil {
					log.Log().Errorf("Error during read file %s, %v", templatePath, err)
					return ""
				}
			} else {
				log.Log().Errorf("Error during read file %s, %v", templatePath, err)
				return ""
			}
		}

		imports = append(imports, string(content))
		return ""
	})

	return processedContent, imports
}
