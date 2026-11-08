package main

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"
)

const cacheBudget = 32 << 20

type cachedResponse struct {
	value any
	size  int
	used  time.Time
}

type pendingResponse struct {
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	waiters int
	value   any
	err     error
}

type responseCache struct {
	mu      sync.Mutex
	ctx     context.Context
	wg      sync.WaitGroup
	closed  bool
	active  int
	values  map[string]cachedResponse
	pending map[string]*pendingResponse
	bytes   int
}

func newResponseCache(ctx context.Context) *responseCache {
	return &responseCache{ctx: ctx, values: map[string]cachedResponse{}, pending: map[string]*pendingResponse{}}
}

func (c *responseCache) close() {
	c.mu.Lock()
	c.closed = true
	for _, p := range c.pending {
		p.cancel()
	}
	c.mu.Unlock()
	c.wg.Wait()
}

func cached[T any](c *responseCache, ctx context.Context, key string, fn func(context.Context) (T, error)) (T, error) {
	value, err := c.do(ctx, key, func(task context.Context) (any, error) { return fn(task) })
	if err != nil {
		var zero T
		return zero, err
	}
	return value.(T), nil
}

func (c *responseCache) do(ctx context.Context, key string, fn func(context.Context) (any, error)) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.closed || c.ctx.Err() != nil {
		c.mu.Unlock()
		return nil, context.Canceled
	}
	if v, ok := c.values[key]; ok {
		v.used = time.Now()
		c.values[key] = v
		c.mu.Unlock()
		return v.value, nil
	}
	p := c.pending[key]
	if p == nil || p.ctx.Err() != nil {
		if c.active >= 8 {
			c.mu.Unlock()
			return nil, fmt.Errorf("查询繁忙，请稍后刷新")
		}
		task, cancel := context.WithCancel(c.ctx)
		p = &pendingResponse{done: make(chan struct{}), ctx: task, cancel: cancel}
		c.pending[key] = p
		c.active++
		c.wg.Add(1)
		go c.compute(key, p, fn)
	}
	p.waiters++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		p.waiters--
		if p.waiters == 0 {
			p.cancel()
		}
		c.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return p.value, p.err
	}
}

func (c *responseCache) compute(key string, p *pendingResponse, fn func(context.Context) (any, error)) {
	defer c.wg.Done()
	defer p.cancel()
	value, err := fn(p.ctx)
	size := 0
	if err == nil && p.ctx.Err() == nil {
		size = int(reflect.TypeOf(value).Size()) + retainedBytes(reflect.ValueOf(value))
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
	if err == nil {
		err = p.ctx.Err()
	}
	p.value, p.err = value, err
	if c.pending[key] == p {
		delete(c.pending, key)
		if err == nil && size <= cacheBudget {
			for len(c.values) >= 64 || c.bytes+size > cacheBudget {
				var oldest string
				var when time.Time
				for k, v := range c.values {
					if oldest == "" || v.used.Before(when) {
						oldest, when = k, v.used
					}
				}
				c.bytes -= c.values[oldest].size
				delete(c.values, oldest)
			}
			c.values[key] = cachedResponse{value: value, size: size, used: time.Now()}
			c.bytes += size
		}
	}
	close(p.done)
}

// Cached view values are acyclic. Count backing storage without encoding it.
// Shared references may be counted twice, which errs toward earlier eviction.
func retainedBytes(v reflect.Value) int {
	switch v.Kind() {
	case reflect.String:
		return v.Len()
	case reflect.Pointer:
		if !v.IsNil() {
			elem := v.Elem()
			return int(elem.Type().Size()) + retainedBytes(elem)
		}
	case reflect.Slice:
		elem := v.Type().Elem()
		size := v.Cap() * int(elem.Size())
		switch elem.Kind() {
		case reflect.String, reflect.Pointer, reflect.Slice, reflect.Struct:
			for i := 0; i < v.Len(); i++ {
				size += retainedBytes(v.Index(i))
			}
		}
		return size
	case reflect.Struct:
		size := 0
		for i := 0; i < v.NumField(); i++ {
			size += retainedBytes(v.Field(i))
		}
		return size
	}
	return 0
}
