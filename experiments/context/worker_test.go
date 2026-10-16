package contextlab

import (
	"context"
	"errors"
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

func TestChildCancelIsolation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	childA, cancelA := context.WithCancel(parent)
	defer cancelA()
	childB, cancelB := context.WithCancel(parent)
	defer cancelB()

	exitedA := make(chan error, 1)
	exitedB := make(chan error, 1)
	go Worker(childA, exitedA)
	go Worker(childB, exitedB)

	cancelA()

	select {
	case err := <-exitedA:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Worker A: unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Worker A did not exit in time")
	}

	if parent.Err() != nil {
		t.Fatalf("parent was unexpectedly canceled: %v", parent.Err())
	}
	if childB.Err() != nil {
		t.Fatalf("childB was unexpectedly canceled: %v", childB.Err())
	}

	select {
	case <-parent.Done():
		t.Fatal("parent was unexpectedly canceled")
	case <-childB.Done():
		t.Fatal("childB was unexpectedly canceled")
	default:
	}

	cancelParent()
	select {
	case err := <-exitedB:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Worker B: unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Worker B did not exit in time")
	}

}
