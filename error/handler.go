package error

import (
	"fmt"
)

func Catch(err any) {
	concreteError, ok := err.(ValidationError)
	if ok {
		fmt.Println(concreteError)
	}
	//handle
}
