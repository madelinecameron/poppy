package agent

import "sync"

// pool runs functions concurrently, at most a fixed number at a time.
type pool struct {
	wg  sync.WaitGroup
	sem chan struct{}
}

func newPool(size int) *pool { return &pool{sem: make(chan struct{}, size)} }

func (p *pool) Go(fn func()) {
	p.wg.Go(func() {
		p.sem <- struct{}{}
		defer func() { <-p.sem }()
		fn()
	})
}

func (p *pool) Wait() { p.wg.Wait() }
