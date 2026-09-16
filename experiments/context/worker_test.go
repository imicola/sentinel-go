package contextlab

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestActiveWorkerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	exited := make(chan error, 1)
	go Worker(ctx, exited)
	cancel()

	select {
	case err := <-exited:
		if err == context.Canceled {
			fmt.Printf("sucess cancel,err = %v", err)
		} else {
			t.Errorf("unsucess cancel,err = %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("worker did not exit")
	}
}

func TestTimeOutWorkerCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		100*time.Millisecond,
	)
	defer cancel()

	exited := make(chan error, 1)
	go Worker(ctx, exited)
	select {
	case err := <-exited:
		if err == context.DeadlineExceeded {
			fmt.Printf("sucess cancel,err = %v", err)
		} else {
			t.Errorf("unsucess cancel,err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not exit")
	}
}

func TestParentContext(t *testing.T) {
	parentCtx, parCancel := context.WithCancel(context.Background())

	childCtx, chiCancel := context.WithCancel(parentCtx)
	defer chiCancel()

	exited := make(chan error)
	go Worker(childCtx, exited)

	parCancel()

	select {
	case err := <-exited:
		if err == context.Canceled {
			fmt.Printf("sucess cancel,err = %v", err)
		} else {
			t.Errorf("unsucess cancel,err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not exit")
	}
}
