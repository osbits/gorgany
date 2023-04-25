package error

import (
	"fmt"
	"runtime"
)

func Catch(err any) {
	fmt.Println(err)
	buf := make([]byte, 1<<16)
	runtime.Stack(buf, true)
	fmt.Printf("%s", buf)
}
