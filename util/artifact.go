package util

import (
	"go/ast"
	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"
	"os"
	"path/filepath"
	"strings"
)

type PkgInfo struct {
	Path    string
	Structs []StructInfo
}

type StructInfo struct {
	Name        string
	Pkg         string
	Annotations []Annotation
}

func (thiz StructInfo) FindAnnotationByName(name string) *Annotation {
	for _, annotation := range thiz.Annotations {
		if annotation.Name == name {
			return &annotation
		}
	}
	return nil
}

type Annotation struct {
	Name      string
	Arguments []Argument
}

type Argument struct {
	Name  string
	Value string
}

type PkgInfos map[string]*PkgInfo

func ScanDir(d string) (PkgInfos, error) {
	pkgInfos := make(PkgInfos)
	err := filepath.Walk(d, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		if filepath.Ext(path) != ".go" {
			return nil
		}

		pkgPath, sts, err := ReadStructsFromFile(path)
		if err != nil {
			return err
		}

		if pkgInfos[pkgPath] == nil {
			pkgInfos[pkgPath] = &PkgInfo{
				Path:    pkgPath,
				Structs: make([]StructInfo, 0),
			}
		}

		pkgInfos[pkgPath].Structs = append(pkgInfos[pkgPath].Structs, sts...)

		return nil
	})

	return pkgInfos, err
}

func ReadStructsFromFile(filePath string) (pkgPath string, structs []StructInfo, err error) {
	splitFilePath := strings.Split(filePath, "/")
	pkgPrefix := strings.Join(splitFilePath[:len(splitFilePath)-2], "/")

	cfg := &packages.Config{
		Mode: packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule | packages.NeedName |
			packages.NeedDeps,
	}

	pkgs, err := packages.Load(cfg, filePath)
	if err != nil {
		return "", nil, err
	}

	if len(pkgs) != 1 {
		return "", nil, nil
	}

	pkg := pkgs[0]

	for _, name := range pkg.Types.Scope().Names() {
		obj := pkg.Types.Scope().Lookup(name)
		if obj == nil {
			continue
		}

		splitStructName := strings.Split(obj.Type().String(), ".")
		structName := splitStructName[len(splitStructName)-1]

		pkgPath = pkgPrefix + "/" + obj.Pkg().Name()

		annotations := parseAnnotations(pkg, structName)

		structs = append(structs, StructInfo{
			Name:        structName,
			Pkg:         obj.Pkg().Name(),
			Annotations: annotations,
		})
	}

	return pkgPath, structs, nil
}

func parseAnnotations(pkg *packages.Package, structName string) []Annotation {
	annotations := make([]Annotation, 0)
	for _, f := range pkg.Syntax {
		comments := ast.NewCommentMap(pkg.Fset, f, f.Comments)
		ast.Inspect(f, func(node ast.Node) bool {
			if typeSpec, ok := node.(*ast.TypeSpec); ok {
				if _, ok := typeSpec.Type.(*ast.StructType); ok {
					_ = pkg.Fset.Position(typeSpec.Pos() - 1)
					for _, commentGroup := range comments.Comments() {
						for _, comment := range commentGroup.List {
							if typeSpec.Name.Name == structName {
								trimAnnotationString := strings.TrimLeft(comment.Text[2:], " ")
								if trimAnnotationString[0] == '@' {
									annotations = append(annotations, Annotation{Name: trimAnnotationString}) //todo need to parse and set arguments
								}
							}
						}
					}
				}
			}
			return true
		})
	}
	return annotations
}

func ModuleName() string {
	modFileContent, err := os.ReadFile("./go.mod")
	if err != nil {
		panic(err)
	}

	modFile, err := modfile.Parse("", modFileContent, nil)
	if err != nil {
		panic(err)
	}

	module := modFile.Module
	if module == nil {
		panic("Module can`t be nil")
	}
	return module.Mod.Path
}
