package worker

import (
	"context"
	"log"
	"sync"
)

// Job represents a unit of work
type Job struct {
	Type    string
	Payload interface{}
}

// Pool manages a pool of worker goroutines
type Pool struct {
	workers int
	jobs    chan Job
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
	handler func(Job)
}

// NewPool creates a new worker pool
func NewPool(workers int, handler func(Job)) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	return &Pool{
		workers: workers,
		jobs:    make(chan Job, 100),
		ctx:     ctx,
		cancel:  cancel,
		handler: handler,
	}
}

// Start begins the worker pool
func (p *Pool) Start() {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	log.Printf("Worker pool started with %d workers", p.workers)
}

// Stop shuts down the worker pool
func (p *Pool) Stop() {
	p.cancel()
	close(p.jobs)
	p.wg.Wait()
	log.Println("Worker pool stopped")
}

// Submit adds a job to the queue
func (p *Pool) Submit(job Job) {
	select {
	case p.jobs <- job:
	default:
		log.Println("Worker pool queue full, dropping job")
	}
}

func (p *Pool) worker(id int) {
	defer p.wg.Done()
	for {
		select {
		case job, ok := <-p.jobs:
			if !ok {
				return
			}
			p.handler(job)
		case <-p.ctx.Done():
			return
		}
	}
}
