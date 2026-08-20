package graphscan

import "os"

type sourceRoot struct{ file *os.File }

func (root *sourceRoot) Close() error { return root.file.Close() }
