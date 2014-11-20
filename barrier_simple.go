package main

import (
	"sync"
)

var (
	barrier    = make(map[string]bool)
	mtxBarrier sync.RWMutex
)

// barrierSet either sets or removes a 'lock' for a given path that indicates
// that the said path corresponds to a file that is not yet complete.
func barrierSet(state bool, path string) {
	mtxBarrier.Lock()
	defer mtxBarrier.Unlock()
	if state {
		barrier[path] = true
	} else {
		delete(barrier, path)
	}
	return
}

// barrierCheck checks if the file corresponding to the given path is currently
// being written to.
func barrierCheck(path string) bool {
	mtxBarrier.RLock()
	defer mtxBarrier.RUnlock()
	_, ok := barrier[path]
	return ok
}
