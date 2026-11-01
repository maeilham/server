package main

import (
	"context"
	"time"
)

// runEvery는 fn을 즉시 한 번 실행한 뒤 interval마다 되풀이하고, ctx가 끝나면 돌아온다. fn이 끝나기 전에는 다음 실행을 시작하지 않는다.
func runEvery(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	fn(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}
