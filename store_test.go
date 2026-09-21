package main

import "testing"

func TestStoreIsolation(t *testing.T) {
	storeA := InitMemoryStore()
	storeB := InitMemoryStore()

	testTR := TargetRequest{
		Name: "test",
		Addr: "test",
	}
	storeA.Create(testTR)
	targetA, errA := storeA.Get(1)
	// targetB, errB := storeB.Get(0)

	if errA != nil {
		t.Errorf("GetA error = %+v", errA)
	}

	// if errB != nil {
	// 	t.Errorf("GetB error = %+v", errB)
	// }

	if targetA.Id != 1 {
		t.Errorf("Testing Error,StoreA ID = %d,not be %d", targetA.Id, 1)
	}
	targetsB := storeB.List()
	if len := len(targetsB); len != 0 {
		t.Errorf("Testing Error,StoreB targets len = %d,not be %d", len, 0)
	}
	testTR2 := TargetRequest{
		Name: "test2",
		Addr: "test2",
	}
	storeB.Create(testTR2)
	targetB2, errB2 := storeB.Get(1)
	targetA2, errA2 := storeA.Get(1)
	if errA2 != nil {
		t.Errorf("GetA error = %+v", errA2)
	}
	if errB2 != nil {
		t.Errorf("GetB error = %+v", errB2)
	}
	if targetB2.Id != 1 {
		t.Errorf("Testing Error,StoreB ID = %d,not be %d", targetB2.Id, 1)
	}
	if targetA != targetA2 {
		t.Errorf("storeA two comparisons different")
	}
}
