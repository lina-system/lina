// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/protocol"
)

func smokeUpdate(ctx context.Context, observer *ctlfiles.Store, r ctlfiles.UpdateRecord) (err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	owner := proc.NewOwner()
	activity, err := lifecycle.NewRegistry(nil).Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "update smoke"})
	if err != nil {
		return err
	}
	defer activity.Release()
	socket := filepath.Join(r.Root, "run/update.sock")
	done := make(chan error, 1)
	go func() {
		done <- owner.Run(ctx, activity, proc.Command{Path: filepath.Join(r.Root, "system/bin/lina"), Args: []string{"core", "--state-root", r.Root, "--socket", socket}, Env: proc.InheritedEnvironment()})
	}()
	joined := false
	defer func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		err = errors.Join(err, owner.Close(cleanup))
		if !joined {
			<-done
		}
	}()
	err = pollUpdate(ctx, func() (bool, error) {
		select {
		case e := <-done:
			joined = true
			return false, fmt.Errorf("update_start_failed: %v", e)
		default:
		}
		record, e := observer.ReadUpdate()
		if e != nil {
			return false, e
		}
		return record != nil && record.ReachedReady, nil
	})
	if err != nil {
		return err
	}
	if err = waitUpdateClient(ctx, socket); err != nil {
		return err
	}
	if err = smokeConversation(ctx, socket, r.ID); err != nil {
		return err
	}
	worker := filepath.Join(r.Root, "system/bin/moirai-worker")
	if info, e := os.Lstat(worker); e == nil && info.Mode().IsRegular() {
		if err = owner.Run(ctx, activity, proc.Command{Path: worker, Args: []string{"--canon", filepath.Join(r.Root, "canon/canon.sqlite"), "--socket", filepath.Join(r.Root, "run/jobs.sock"), "--preflight"}, Env: []string{}}); err != nil {
			return fmt.Errorf("update_worker_not_ready: %w", err)
		}
	}
	if err = drainCore(ctx, socket); err != nil {
		return err
	}
	select {
	case err = <-done:
		joined = true
	case <-ctx.Done():
		return ctx.Err()
	}
	var exited interface{ ExitCode() int }
	if !errors.As(err, &exited) || exited.ExitCode() != 75 {
		return fmt.Errorf("update_smoke_shutdown_failed: %v", err)
	}
	return nil
}

func smokeConversation(ctx context.Context, socket, id string) error {
	c, err := clientapi.DialWithCapabilities(ctx, socket, "update", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err = c.History(ctx, protocol.HistoryGet{}); err != nil {
		return err
	}
	frame, err := c.Receive(ctx)
	if err != nil {
		return err
	}
	history, ok := frame.Payload.(protocol.History)
	if !ok || history.ConversationID == nil {
		return errors.New("update_conversation_not_ready")
	}
	topic := "conversation:" + *history.ConversationID
	if _, err = c.Subscribe(ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		return err
	}
	if _, err = c.Receive(ctx); err != nil {
		return err
	}
	prompt := "Confirm that you can answer this update readiness check. Do not call tools or take external actions."
	key := "update-smoke-" + id
	request, err := c.Input(ctx, protocol.Input{ConversationID: history.ConversationID, InputID: &key, Text: &prompt})
	if err != nil {
		return err
	}
	acknowledged, answer, finished := false, false, false
	for !acknowledged || !finished {
		frame, err = c.Receive(ctx)
		if err != nil {
			return err
		}
		if frame.Error != nil {
			return fmt.Errorf("update_conversation_not_ready: %s", frame.Error.Code)
		}
		if frame.Kind == protocol.KindReadinessFailure {
			return errors.New("update_conversation_not_ready")
		}
		if frame.ReplyTo != nil && *frame.ReplyTo == request {
			acknowledged = true
		}
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerAgent && item.Text != nil && *item.Text != "" {
			answer = true
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			finished = true
		}
	}
	if !answer {
		return errors.New("update_conversation_not_ready")
	}
	return nil
}
