package contextlab

import "context"

func Worker(ctx context.Context, exited chan<- error) {
	<-ctx.Done()
	exited <- ctx.Err()
}
