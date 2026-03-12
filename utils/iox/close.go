package iox

import "io"

func CloseAndCapture(c io.Closer, err *error) {
	closeErr := c.Close()
	if *err == nil {
		*err = closeErr
	}
}
