package domain

import (
	"bytes"
	"context"
	"fmt"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/util"
	"go/format"
	"os"
	"path/filepath"
	"runtime"
	"text/template"
)

type RegisterDomainsCommand struct {
	domainContext core.IDomainContext `container:"inject"`
}

func (thiz RegisterDomainsCommand) GetName() string {
	return "domains:register"
}

func (thiz RegisterDomainsCommand) Execute(ctx context.Context) {
	needToRegenerate := false

	pkgInfos, err := util.ScanDir("./pkg/domain")
	if err != nil {
		panic(err)
	}

	moduleName := util.ModuleName()

	registeredModels := thiz.domainContext.GetDomains()

	registers := make([]string, 0)
	imports := make([]string, 0)
	for pkgPath, pkgInfo := range pkgInfos {
		modelsInPkg := 0
		for _, model := range pkgInfo.Structs {
			if model.FindAnnotationByName("@Embedded") != nil || model.FindAnnotationByName("@Abstract") != nil {
				continue
			}

			key := moduleName + "/" + pkgPath + "." + model.Name
			_, ok := registeredModels[key]
			if !ok {
				needToRegenerate = true
			}
			registers = append(registers, fmt.Sprintf("\"%s\": %s{},", key, model.Pkg+"."+model.Name))
			modelsInPkg++
		}
		if modelsInPkg > 0 {
			imports = append(imports, moduleName+"/"+pkgPath)
		}
	}

	if needToRegenerate {
		err = thiz.generateRegistrar(imports, registers)
		if err != nil {
			panic(err)
		}
	}
}

func (thiz RegisterDomainsCommand) generateRegistrar(imports []string, registers []string) error {
	_, callerFilename, _, _ := runtime.Caller(0)
	dir := filepath.Dir(callerFilename)

	content, err := os.ReadFile(filepath.Join(dir, "../../resource/template/command/domain_registrar.html"))
	if err != nil {
		panic(err)
	}

	tpl, err := template.New("domain_registrar").Parse(string(content))
	if err != nil {
		panic(err)
	}

	writer := new(bytes.Buffer)
	err = tpl.Execute(writer, map[string]any{"Imports": imports, "Registers": registers})
	if err != nil {
		panic(err)
	}

	// gofmt the output. The template's indentation is not gofmt's, and a generated file
	// that fails `gofmt -l` fails every CI format gate the moment it is regenerated.
	source, err := format.Source(writer.Bytes())
	if err != nil {
		return fmt.Errorf("domains:register: generated pkg/provider/domains.go does not parse: %w", err)
	}

	// 0644, not os.ModePerm: a source file has no business being executable or
	// world-writable.
	return os.WriteFile("pkg/provider/domains.go", source, 0o644)
}
