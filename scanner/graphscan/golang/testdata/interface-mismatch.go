package p

type Runner interface {
	Run(int) error
}

type Wrong struct{}

func (Wrong) Run(string) error { return nil }
