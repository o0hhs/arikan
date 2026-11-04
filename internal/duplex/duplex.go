// Package duplex copies one connection onto another until both directions are done,
// closing each direction rather than the whole connection: a client that has finished
// sending still gets its answer (an HTTP request, a TLS shutdown), and the peer learns the
// end of the stream the same way.
package duplex

import (
	"io"
	"sync"
)

// Copy copies a and b onto each other until both directions end, then closes both, and
// returns what each direction carried. A side that can close its write half alone does, so
// the other end sees the end of the stream and not a cut connection.
func Copy(a, b io.ReadWriteCloser) (aToB, bToA int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		aToB, _ = io.Copy(b, a)
		closeWrite(b)
	}()
	go func() {
		defer wg.Done()
		bToA, _ = io.Copy(a, b)
		closeWrite(a)
	}()
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
	return aToB, bToA
}

func closeWrite(c io.ReadWriteCloser) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
