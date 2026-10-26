package qqmusic

import (
	"context"
	"sync"
	"time"
)

const defaultEnrollmentTimeout = 180 * time.Second

type enrollmentSession struct {
	channel    string
	identifier string
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	polls      sync.WaitGroup
}
