package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type cachedResponse struct {
	body []byte
	used time.Time
}
type pendingResponse struct {
	done      chan struct{}
	cancel    context.CancelFunc
	waiters   int
	abandoned bool
	body      []byte
	err       error
}
type responseCache struct {
	mu      sync.Mutex
	values  map[string]cachedResponse
	pending map[string]*pendingResponse
	bytes   int
}

func newResponseCache() *responseCache {
	return &responseCache{values: map[string]cachedResponse{}, pending: map[string]*pendingResponse{}}
}
func (c *responseCache) Do(ctx context.Context, key string, fn func(context.Context) ([]byte, error)) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if v, ok := c.values[key]; ok {
		v.used = time.Now()
		c.values[key] = v
		c.mu.Unlock()
		return v.body, nil
	}
	p, ok := c.pending[key]
	if ok && p.abandoned {
		delete(c.pending, key)
		ok = false
	}
	if !ok {
		if len(c.pending) >= 8 {
			c.mu.Unlock()
			return nil, fmt.Errorf("查询繁忙，请稍后刷新")
		}
		task, cancel := context.WithCancel(context.Background())
		p = &pendingResponse{done: make(chan struct{}), cancel: cancel}
		c.pending[key] = p
		go func() {
			body, err := fn(task)
			c.mu.Lock()
			defer c.mu.Unlock()
			p.body, p.err = body, err
			if err == nil && len(body) <= 8<<20 {
				for len(c.values) >= 64 || c.bytes+len(body) > 32<<20 {
					var oldest string
					var when time.Time
					for k, v := range c.values {
						if oldest == "" || v.used.Before(when) {
							oldest, when = k, v.used
						}
					}
					if oldest == "" {
						break
					}
					c.bytes -= len(c.values[oldest].body)
					delete(c.values, oldest)
				}
				if previous, exists := c.values[key]; exists {
					c.bytes -= len(previous.body)
				}
				c.values[key] = cachedResponse{body: body, used: time.Now()}
				c.bytes += len(body)
			}
			if c.pending[key] == p {
				delete(c.pending, key)
			}
			close(p.done)
			cancel()
		}()
	}
	p.waiters++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		p.waiters--
		if p.waiters == 0 {
			p.abandoned = true
			p.cancel()
		}
		c.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return p.body, p.err
	}
}
