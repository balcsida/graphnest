package service

import logging "example.com/log"

type Base interface {
	Close()
}

type Worker interface {
	Base
	Run()
}

type Runner struct{}
type Partial struct{}

func (Runner) Run()   {}
func (Runner) Close() {}
func (Partial) Run()  {}

func Start() {
	logging.Print("start")
	Runner{}.Run()
}
