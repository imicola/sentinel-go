package main

import (
	"errors"
	"sync"
)

var (
	ErrTargetNotFound = errors.New("target not found")
	ErrInvalidTarget  = errors.New("name and address are required")
)

type MemoryStore struct {
	targets map[int]Target
	nextID  int
	mu      sync.Mutex
}

func InitMemoryStore() *MemoryStore {
	return &MemoryStore{
		targets: make(map[int]Target),
		nextID:  0,
	}
}

func (store *MemoryStore) Create(req TargetRequest) Target {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.nextID += 1
	store.targets[store.nextID] = Target{
		Id:   store.nextID,
		Name: req.Name,
		Addr: req.Addr,
	}
	return store.targets[store.nextID]
}

func (store *MemoryStore) List() []Target {
	store.mu.Lock()
	list := make([]Target, 0, len(store.targets))
	for _, target := range store.targets {
		list = append(list, target)
	}
	store.mu.Unlock()
	return list
}

func (store *MemoryStore) Get(id int) (Target, error) {
	store.mu.Lock()
	res, ok := store.targets[id]
	store.mu.Unlock()
	if !ok {
		err := errors.New("invalid user id")
		return res, err
	}
	return res, nil
}

func (store *MemoryStore) Update(id int, req TargetRequest) (Target, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	target, ok := store.targets[id]

	if !ok {
		return target, ErrTargetNotFound
	}

	if req.Name == "" || req.Addr == "" {
		return target, ErrInvalidTarget
	}
	store.targets[id] = Target{
		Name: req.Name,
		Addr: req.Addr,
		Id:   target.Id,
	}
	return store.targets[id], nil
}

func (store *MemoryStore) Delete(id int) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	_, ok := store.targets[id]
	if !ok {
		err := errors.New("invaild target")
		return err
	}
	delete(store.targets, id)
	return nil
}
