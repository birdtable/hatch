package tests

import "io"

// live is the Stdout of the run under --stream. It answers a writer, which go-proc gives the
// `go test -json` stream as it reads it, in pieces that need not end on a line, and the call
// that ends the read once the run is over. What is written is scanned as it arrives, and a
// line for each package is printed to out at its pass, fail or skip event: a piece of a line
// waits for the rest, and a last line the stream did not end is read when the read is ended.
// The stream itself is the run's, kept whole and shown on a red as it is without --stream.
func live(out io.Writer) (io.Writer, func()) {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = scan(pr, out)
		// A scan that stopped early must never hold the run: what is left is read and dropped.
		_, _ = io.Copy(io.Discard, pr)
	}()
	return pw, func() {
		_ = pw.Close()
		<-done
	}
}
