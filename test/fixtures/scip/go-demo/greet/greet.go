package greet

import "fmt"

// Greeter greets.
type Greeter interface {
	Greet(name string) string
}

// Hello greets by name.
func Hello(name string) string {
	return fmt.Sprintf("hello %s", name)
}

type Impl struct{ Prefix string }

func (i Impl) Greet(name string) string { return i.Prefix + Hello(name) }
