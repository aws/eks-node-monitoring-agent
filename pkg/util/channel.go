package util

// Sink creates a message sink that merges different channels together in a
// non-determinisitic, unordered fashion
func Sink[T any](chans ...<-chan T) <-chan T {
	sink := make(chan T)
	for _, c := range chans {
		go func() {
			for x := range c {
				sink <- x
			}
		}()
	}
	return sink
}
