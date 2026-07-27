package p

type Closer interface {
	Close()
}

type PointerOnly struct{}

func (*PointerOnly) Close() {}
