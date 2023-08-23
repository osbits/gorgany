package command

import (
	"flag"
	"github.com/go-playground/validator/v10"
	"gorgany/internal"
	"gorgany/log"
	"gorgany/proxy"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

type FlagConfig struct { //`command:"flag,default=value,name=name"`
	Name         string
	DefaultValue string
	Type         string
	Description  string
	Value        any
}

type Flags map[string]*FlagConfig //[fieldName]FlagConfig

type Resolver struct {
}

func NewCommandResolver() *Resolver {
	return &Resolver{}
}

func (thiz Resolver) ResolveCommand(commandName string) proxy.ICommand {
	command := internal.GetFrameworkRegistrar().GetCommand(commandName)
	if command == nil {
		log.Log("").Panicf("Command %s does not exist", commandName)
	}
	rvCommand := reflect.ValueOf(command)
	if rvCommand.Kind() == reflect.Ptr {
		rvCommand = rvCommand.Elem()
	}

	rtCommand := rvCommand.Type()
	commandFlags := flag.NewFlagSet(commandName, flag.ExitOnError)

	flags := thiz.parseDefinedFlags(rtCommand)
	flags = thiz.parseInputFlags(flags, commandFlags)

	for fieldName, flagConfig := range flags {
		rvField := rvCommand.FieldByName(fieldName)
		rvField.Set(reflect.ValueOf(flagConfig.Value).Elem())
	}

	validate := validator.New()
	err := validate.Struct(command)
	if err != nil {
		commandFlags.PrintDefaults()

		if _, ok := err.(*validator.InvalidValidationError); ok {
			log.Log("").Panicf("Invalid validation error: %v", err)
		}

		for _, err := range err.(validator.ValidationErrors) {
			log.Log("").Panicf("Invalid argument for %s flag, %v", flags[err.Field()].Name, err)
		}
	}

	return command
}

func (thiz Resolver) parseDefinedFlags(rtCommand reflect.Type) Flags {
	flags := make(Flags)
	for i := 0; i < rtCommand.NumField(); i++ {
		field := rtCommand.Field(i)
		vals, ok := field.Tag.Lookup("command")
		if !ok {
			continue
		}

		flagRegExp := regexp.MustCompile(".*flag.*")
		if !flagRegExp.Match([]byte(vals)) {
			continue
		}

		nameRegExp := regexp.MustCompile("name=.+")
		defaultValueRegExp := regexp.MustCompile("default=.+")
		descriptionRegExp := regexp.MustCompile("description=.+")
		flagConfig := FlagConfig{Type: field.Type.Name()}
		for _, val := range strings.Split(vals, ",") {
			if nameRegExp.Match([]byte(val)) {
				splitVal := strings.Split(val, "=")
				flagConfig.Name = splitVal[1]
			}

			if defaultValueRegExp.Match([]byte(val)) {
				splitVal := strings.Split(val, "=")
				flagConfig.DefaultValue = splitVal[1]
			}

			if descriptionRegExp.Match([]byte(val)) {
				splitVal := strings.Split(val, "=")
				flagConfig.Description = splitVal[1]
			}
		}
		if flagConfig.Name == "" {
			flagConfig.Name = field.Name
		}

		flags[field.Name] = &flagConfig
	}

	return flags
}

func (thiz Resolver) parseInputFlags(flags Flags, commandFlags *flag.FlagSet) Flags {
	for fieldName, _ := range flags {
		flagConfig := flags[fieldName]

		var value any
		var convertError error

		switch flagConfig.Type {
		case "string":
			value = commandFlags.String(flagConfig.Name, flagConfig.DefaultValue, flagConfig.Description)
			break
		case "int", "int8", "int16", "int32":
			defaultValue := 0
			if flagConfig.DefaultValue != "" {
				defaultValue, convertError = strconv.Atoi(flagConfig.DefaultValue)
				if convertError != nil {
					panic(convertError)
				}
			}
			value = commandFlags.Int(flagConfig.Name, defaultValue, flagConfig.Description)
			break
		case "int64":
			defaultValue := int64(0)
			if flagConfig.DefaultValue != "" {
				defaultValue, convertError = strconv.ParseInt(flagConfig.DefaultValue, 10, 64)
				if convertError != nil {
					panic(convertError)
				}
			}
			value = commandFlags.Int64(flagConfig.Name, defaultValue, flagConfig.Description)
			break
		case "uint", "uint8", "uint16", "uint32":
			defaultValue := uint64(0)
			if flagConfig.DefaultValue != "" {
				defaultValue, convertError = strconv.ParseUint(flagConfig.DefaultValue, 10, 64)
				if convertError != nil {
					panic(convertError)
				}
			}
			value = commandFlags.Uint(flagConfig.Name, uint(defaultValue), flagConfig.Description)
			break
		case "uint64":
			defaultValue := uint64(0)
			if flagConfig.DefaultValue != "" {
				defaultValue, convertError = strconv.ParseUint(flagConfig.DefaultValue, 10, 64)
				if convertError != nil {
					panic(convertError)
				}
			}
			value = commandFlags.Uint64(flagConfig.Name, defaultValue, flagConfig.Description)
			break
		case "float32", "float64":
			defaultValue := float64(0)
			if flagConfig.DefaultValue != "" {
				defaultValue, convertError = strconv.ParseFloat(flagConfig.DefaultValue, 64)
				if convertError != nil {
					panic(convertError)
				}
			}
			value = commandFlags.Float64(flagConfig.Name, defaultValue, flagConfig.Description)
			break
		case "bool":
			defaultValue := false
			if flagConfig.DefaultValue != "" {
				defaultValue, convertError = strconv.ParseBool(flagConfig.DefaultValue)
				if convertError != nil {
					panic(convertError)
				}
			}
			value = commandFlags.Bool(flagConfig.Name, defaultValue, flagConfig.Description)
			break
		}

		flagConfig.Value = value
	}

	err := commandFlags.Parse(os.Args[2:])
	if err != nil {
		panic(err)
	}

	return flags
}
