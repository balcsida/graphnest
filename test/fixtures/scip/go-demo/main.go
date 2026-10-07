package main

import (
	"fmt"

	"github.com/oldorg/demo/greet"
)

func main() {
	fmt.Println(greet.Hello("world"))
	var g greet.Greeter = greet.Impl{Prefix: "> "}
	fmt.Println(g.Greet("x"))
}
