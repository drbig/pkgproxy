package main

import (
	"sync"
)

var (
	barrier    = make(map[string]bool)
	mtxBarrier sync.RWMutex
)

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

func barrierCheck(path string) bool {
	mtxBarrier.RLock()
	defer mtxBarrier.RUnlock()
	_, ok := barrier[path]
	return ok
}
