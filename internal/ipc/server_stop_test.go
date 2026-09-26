package ipc

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/dto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestStopCancelsOpenStatusStreams locks that shutdown never waits forever on a connected GUI's status stream.
func TestStopCancelsOpenStatusStreams(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "gzr-stop-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	previous := gracefulStopTimeout
	gracefulStopTimeout = 200 * time.Millisecond
	t.Cleanup(func() { gracefulStopTimeout = previous })

	fake := &fakeController{statusCh: make(chan dto.StatusEventResult, 1)}
	sock := filepath.Join(dir, "gorganizer.sock")
	srv := NewServer(sock, fake)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	stream, err := pb.NewGorganizerClient(conn).WatchStatus(context.Background(), &pb.WatchStatusRequest{})
	if err != nil {
		t.Fatalf("WatchStatus: %v", err)
	}
	fake.statusCh <- dto.StatusEventResult{Info: "ready"}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("first Recv: %v", err)
	}

	stopped := make(chan struct{})
	go func() {
		srv.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return while a status stream stayed open")
	}
	if _, err := stream.Recv(); err == nil {
		t.Error("status stream still delivering after Stop")
	}
}

type blockingStatusController struct {
	*fakeController
	entered chan struct{}
	release chan struct{}
}

// GetVFSStatus blocks until the test releases it, like a handler waiting on a daemon lock during shutdown.
func (b *blockingStatusController) GetVFSStatus(string) (*dto.VFSStatusResult, error) {
	close(b.entered)
	<-b.release
	return &dto.VFSStatusResult{}, nil
}

// TestStopAbandonsHandlersThatOutliveTheForcedStop locks that Stop returns within its bound while a unary handler stays blocked.
func TestStopAbandonsHandlersThatOutliveTheForcedStop(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "gzr-stop-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	previousGraceful, previousForced := gracefulStopTimeout, forcedStopWait
	gracefulStopTimeout, forcedStopWait = 100*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { gracefulStopTimeout, forcedStopWait = previousGraceful, previousForced })

	ctrl := &blockingStatusController{
		fakeController: &fakeController{statusCh: make(chan dto.StatusEventResult, 1)},
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(ctrl.release) }) }
	t.Cleanup(releaseHandler)
	sock := filepath.Join(dir, "gorganizer.sock")
	srv := NewServer(sock, ctrl)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		_, _ = pb.NewGorganizerClient(conn).GetVFSStatus(context.Background(), &pb.GetVFSStatusRequest{GameId: "stardewvalley"})
	}()
	select {
	case <-ctrl.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("GetVFSStatus handler never started")
	}

	stopped := make(chan struct{})
	started := time.Now()
	go func() {
		srv.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		releaseHandler()
		t.Fatal("Stop waited for a handler that never returns")
	}
	if elapsed := time.Since(started); elapsed < gracefulStopTimeout+forcedStopWait {
		t.Errorf("Stop returned after %v, before its graceful and forced bounds", elapsed)
	}
}
