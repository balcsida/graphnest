package fixture

type Worker interface {
	Run()
}

type Service struct{}

func (Service) Run() {}

func CrossFile() {}
