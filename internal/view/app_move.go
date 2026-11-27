package view

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zhecho/m9s/internal/mail"
	msync "github.com/zhecho/m9s/internal/sync"
	"github.com/zhecho/m9s/internal/ui"
)

const (
	archiveHint = "archive: not available for this account"
	moveHint    = "move: not available for this account"
)

// selectedMessage is the single message (not a conversation row) star,
// archive and move act on: the selected row of the mail list or preview.
func (ra *RootApp) selectedMessage() (*mail.Account, msync.Account, *mail.Folder, *mail.Envelope, bool) {
	acct, env, folder := ra.manager.Active(), ra.selectedEnv, ra.currentFolder
	if acct == nil || env == nil || folder == nil || ra.pane == paneFolders {
		return nil, nil, nil, nil, false
	}
	eng, ok := ra.engines[acct.Name]
	return acct, eng, folder, env, ok
}

// setFlag shows env with flag on or off.
func (ra *RootApp) setFlag(env *mail.Envelope, flag string, on bool) {
	flags := slices.DeleteFunc(slices.Clone(env.Flags), func(f string) bool { return f == flag })
	if on {
		flags = append(flags, flag)
	}
	env.Flags = flags
	ra.maillist.Redraw()
}

// starSelected handles * on a single message: it toggles \Flagged, shown at
// once and put back if the server refuses.
func (ra *RootApp) starSelected() bool {
	acct, eng, folder, env, ok := ra.selectedMessage()
	if !ok {
		return false
	}
	on := !env.IsFlagged()
	ra.setFlag(env, `\Flagged`, on)
	done := "Star removed"
	if on {
		done = "Starred"
	}
	name, id := folder.Name, env.ID
	go func() {
		err := eng.SetFlags(context.Background(), name, id, []string{`\Flagged`}, on)
		ra.uiApp.QueueUpdateDraw(func() {
			if err != nil {
				ui.AppLog.Error("[%s] star failed: %v", acct.Name, err)
				ra.uiApp.StatusBar.Error("Failed: " + err.Error())
				ra.setFlag(env, `\Flagged`, !on)
				return
			}
			ra.uiApp.StatusBar.Info(done)
		})
	}()
	return true
}

// archiveSelected handles a on a single message.
func (ra *RootApp) archiveSelected() bool {
	acct, eng, folder, env, ok := ra.selectedMessage()
	if !ok {
		return false
	}
	mv, ok := eng.(msync.Mover)
	if !ok {
		ra.uiApp.StatusBar.Info(archiveHint)
		return true
	}
	id := env.ID
	ra.applyChange("archive", acct, eng, folder, "Archived", true, func(ctx context.Context) error {
		return mv.Archive(ctx, folder.Name, id)
	})
	return true
}

// openMovePicker handles m: a list of the folders the selected message (or
// conversation) can go to; Enter moves it there, Esc gives the keyboard
// back.
func (ra *RootApp) openMovePicker() {
	acct, eng, folder, env, ok := ra.selectedMessage()
	if !ok {
		return
	}
	mv, ok := eng.(msync.Mover)
	if !ok {
		ra.uiApp.StatusBar.Info(moveHint)
		return
	}
	targets := mv.MoveTargets(folder.Name)
	if len(targets) == 0 {
		ra.uiApp.StatusBar.Info("move: no other folder")
		return
	}
	marked := ra.markedMessages()
	title := "Move to"
	if len(marked) > 0 {
		title = "Move " + ra.countText(acct, folder, len(marked)) + " to"
	}
	previous := ra.uiApp.GetFocus()
	closePicker := func() {
		ra.uiApp.Pages.RemovePage(pageMovePicker)
		ra.movePicker = nil
		if previous != nil {
			ra.uiApp.SetFocus(previous)
		}
	}
	ra.movePicker = NewFolderPicker(ra.uiApp.Skin, title, targets, func(dest string) {
		closePicker()
		if len(marked) > 0 {
			ra.moveMarked(acct, eng, mv, folder, marked, dest)
			return
		}
		ra.moveTo(acct, eng, mv, folder, env, dest)
	}, closePicker)
	ra.uiApp.Pages.AddPage(pageMovePicker, ra.movePicker, true, true)
	ra.uiApp.SetFocus(ra.movePicker)
}

// moveTo moves env out of folder to dest: the whole conversation on a
// conversation row, else the one message.
func (ra *RootApp) moveTo(acct *mail.Account, eng msync.Account, mv msync.Mover, folder *mail.Folder, env *mail.Envelope, dest string) {
	done := "Moved to " + dest
	change := func(ctx context.Context) error { return mv.Move(ctx, folder.Name, env.ID, dest) }
	if th, ok := eng.(msync.Threads); ok && env.ThreadID != "" && ra.conversationRows(acct.Name, folder.Name) {
		change = func(ctx context.Context) error {
			return th.ModifyThread(ctx, env.ThreadID, []string{dest}, []string{folder.Name})
		}
	}
	ra.applyChange("move", acct, eng, folder, done, true, func(ctx context.Context) error {
		err := change(ctx)
		if err == nil {
			go eng.RefreshBadge(context.Background(), dest)
		}
		return err
	})
}

// markedMessages returns the messages marked in the mail list (space or a
// modifier click), which m and d then act on instead of the cursor row. The
// reader shows one message, so marks do not apply while it is open.
func (ra *RootApp) markedMessages() []*mail.Envelope {
	if page, _ := ra.uiApp.Pages.GetFrontPage(); page != pageMain {
		return nil
	}
	return ra.maillist.Marked()
}

// countText says "1 message", "3 messages", or "conversations" on a list of
// conversations.
func (ra *RootApp) countText(acct *mail.Account, folder *mail.Folder, n int) string {
	noun := "message"
	if _, ok := ra.engines[acct.Name].(msync.Threads); ok && ra.conversationRows(acct.Name, folder.Name) {
		noun = "conversation"
	}
	if n != 1 {
		noun += "s"
	}
	return thousands(n) + " " + noun
}

// moveMarked moves every marked message (or conversation) out of folder to
// dest.
func (ra *RootApp) moveMarked(acct *mail.Account, eng msync.Account, mv msync.Mover, folder *mail.Folder, envs []*mail.Envelope, dest string) {
	th, threads := eng.(msync.Threads)
	threads = threads && ra.conversationRows(acct.Name, folder.Name)
	src := folder.Name
	ra.applyToMarked("move", acct, eng, folder, envs,
		func(n int) string { return "Moved " + ra.countText(acct, folder, n) + " to " + dest },
		func(ctx context.Context, env *mail.Envelope) error {
			if threads && env.ThreadID != "" {
				return th.ModifyThread(ctx, env.ThreadID, []string{dest}, []string{src})
			}
			return mv.Move(ctx, src, env.ID, dest)
		},
		func() { eng.RefreshBadge(context.Background(), dest) })
}

// deleteMarked handles d with messages marked: it asks once for all of
// them. In the trash they are deleted for good (the dialog starts on
// Cancel), elsewhere they move to the trash; conversations move to the
// trash as a whole, as one d on a conversation row does. Accounts that can
// do it in a few requests get the lot at once (BulkDeleter, BulkTrasher,
// BulkThreadTrasher).
func (ra *RootApp) deleteMarked(acct *mail.Account, eng msync.Account, folder *mail.Folder, envs []*mail.Envelope) {
	what := ra.countText(acct, folder, len(envs))
	trash := eng.TrashFolderName()
	if th, ok := eng.(msync.Threads); ok && ra.conversationRows(acct.Name, folder.Name) {
		ra.showDeleteModal(fmt.Sprintf("Move %s to %s?", what, trash), []string{"Delete", "Cancel"}, func() {
			done := func(n int) string { return "Moved " + ra.countText(acct, folder, n) + " to " + trash }
			if bulk, ok := eng.(msync.BulkThreadTrasher); ok { // a few requests for the lot
				ra.runOnMarked("delete", acct, eng, folder, envs, done,
					func(ctx context.Context, envs []*mail.Envelope, progress func(int)) map[string]error {
						return trashConversations(ctx, eng, bulk, folder.Name, envs, progress)
					}, nil, false)
				return
			}
			ra.applyToMarked("delete", acct, eng, folder, envs, done,
				func(ctx context.Context, env *mail.Envelope) error {
					if env.ThreadID == "" {
						return eng.MoveToTrash(ctx, folder.Name, env.ID)
					}
					return th.TrashThread(ctx, env.ThreadID)
				}, nil)
		})
		return
	}
	if trash == folder.Name {
		ra.showDeleteDialog(
			fmt.Sprintf("Permanently delete %s?\nThis cannot be undone.", what),
			[]ui.DialogButton{{Label: "Delete Forever", Danger: true}, {Label: "Cancel"}},
			1, // Cancel: a stray Enter must not delete them all
			func(label string) {
				if label != "Delete Forever" {
					return
				}
				done := func(n int) string { return "Permanently deleted " + ra.countText(acct, folder, n) }
				if bulk, ok := eng.(msync.BulkDeleter); ok { // a few requests for the lot
					ra.runOnMarked("delete", acct, eng, folder, envs, done,
						func(ctx context.Context, envs []*mail.Envelope, progress func(int)) map[string]error {
							ids := make([]string, len(envs))
							for i, env := range envs {
								ids[i] = env.ID
							}
							return bulk.PermanentDeleteMany(ctx, folder.Name, ids, progress)
						}, nil, false)
					return
				}
				ra.applyToMarked("delete", acct, eng, folder, envs, done,
					func(ctx context.Context, env *mail.Envelope) error {
						return eng.PermanentDelete(ctx, folder.Name, env.ID)
					}, nil)
			})
		return
	}
	ra.showDeleteModal(fmt.Sprintf("Move %s to %s?", what, trash), []string{"Delete", "Cancel"}, func() {
		done := func(n int) string { return "Moved " + ra.countText(acct, folder, n) + " to " + trash }
		if bulk, ok := eng.(msync.BulkTrasher); ok { // a few requests for the lot
			ra.runOnMarked("delete", acct, eng, folder, envs, done,
				func(ctx context.Context, envs []*mail.Envelope, progress func(int)) map[string]error {
					ids := make([]string, len(envs))
					for i, env := range envs {
						ids[i] = env.ID
					}
					return bulk.MoveToTrashMany(ctx, folder.Name, ids, progress)
				}, nil, false)
			return
		}
		ra.applyToMarked("delete", acct, eng, folder, envs, done,
			func(ctx context.Context, env *mail.Envelope) error {
				return eng.MoveToTrash(ctx, folder.Name, env.ID)
			}, nil)
	})
}

// trashConversations moves conversation rows to the trash as wholes with
// one bulk call (a row with no conversation goes alone) and returns the rows
// that failed, with why.
func trashConversations(ctx context.Context, eng msync.Account, bulk msync.BulkThreadTrasher, folder string,
	envs []*mail.Envelope, progress func(int)) map[string]error {
	failed := make(map[string]error)
	var threads []string
	rows := make(map[string][]string) // thread id -> its rows
	for _, env := range envs {
		if env.ThreadID == "" {
			if err := eng.MoveToTrash(ctx, folder, env.ID); err != nil {
				failed[env.ID] = err
			}
			progress(1)
			continue
		}
		if _, seen := rows[env.ThreadID]; !seen {
			threads = append(threads, env.ThreadID)
		}
		rows[env.ThreadID] = append(rows[env.ThreadID], env.ID)
	}
	if len(threads) == 0 {
		return failed
	}
	for t, err := range bulk.TrashThreadsMany(ctx, threads, progress) {
		for _, id := range rows[t] {
			failed[id] = err
		}
	}
	return failed
}

// applyToMarked runs op on each of envs off the UI goroutine, one after the
// other, then says how many it did (done) or what failed, unmarks the ones
// done and reloads folder's list and counts. after, if set, runs off the UI
// goroutine once something was done (e.g. the destination's count).
//
// keep leaves the done ones marked: after u or * they are still in the list,
// ready for the next action on the same selection.
func (ra *RootApp) applyToMarked(tag string, acct *mail.Account, eng msync.Account, folder *mail.Folder, envs []*mail.Envelope,
	done func(n int) string, op func(context.Context, *mail.Envelope) error, after func(), keep ...bool) {
	each := func(ctx context.Context, envs []*mail.Envelope, progress func(int)) map[string]error {
		failed := make(map[string]error)
		for _, env := range envs {
			if err := op(ctx, env); err != nil {
				failed[env.ID] = err
			}
			progress(1)
		}
		return failed
	}
	ra.runOnMarked(tag, acct, eng, folder, envs, done, each, after, len(keep) > 0 && keep[0])
}

// runOnMarked is applyToMarked with run doing the work on all of envs at
// once: it tells progress how many more it dealt with and returns the ids
// that failed, with why.
func (ra *RootApp) runOnMarked(tag string, acct *mail.Account, eng msync.Account, folder *mail.Folder, envs []*mail.Envelope,
	done func(n int) string, run func(context.Context, []*mail.Envelope, func(int)) map[string]error, after func(), keep bool) {
	// One bulk action at a time: a selection of thousands takes a while, and
	// pressing the key again must not queue the same work twice.
	if ra.bulkBusy {
		ra.uiApp.StatusBar.Info("Still working on the last action; try again when it is done")
		return
	}
	ra.bulkBusy = true
	total := ra.countText(acct, folder, len(envs))
	ra.uiApp.StatusBar.Info(bulkDoing(tag, total))
	var reached atomic.Int64 // messages tried so far, done or failed
	stopProgress := ra.bulkProgress(acct, tag, total, &reached)
	go func() {
		errs := run(context.Background(), envs, func(n int) { reached.Add(int64(n)) })
		var doneIDs []string
		var firstErr error
		failed := 0
		for _, env := range envs {
			if err, bad := errs[env.ID]; bad {
				ui.AppLog.Error("[%s] %q failed: %v", tag, env.Subject, err)
				if firstErr == nil {
					firstErr = err
				}
				failed++
				continue
			}
			doneIDs = append(doneIDs, env.ID)
		}
		stopProgress()
		ra.onUI(func() {
			ra.bulkBusy = false
			if !keep {
				ra.maillist.Unmark(doneIDs...)
			}
			switch {
			case failed == 0:
				ui.AppLog.Info("[%s] %s", tag, done(len(doneIDs)))
				ra.uiApp.StatusBar.Info(done(len(doneIDs)))
			case len(doneIDs) == 0:
				ra.uiApp.StatusBar.Error("Failed: " + failureText(firstErr))
			default:
				ra.uiApp.StatusBar.Error(fmt.Sprintf("%s; %d failed: %s", done(len(doneIDs)), failed, failureText(firstErr)))
			}
		})
		if len(doneIDs) > 0 && after != nil {
			after()
		}
		// Reload even when everything failed: a message no longer in the
		// trash, say, is shown where it is now.
		go eng.RefreshBadge(context.Background(), folder.Name)
		ra.loadEnvelopes(acct, folder)
	}()
}

// actOnMarked handles a, u and * with messages marked: archive all of them,
// mark all of them unread, or star all of them — unstar them when every one
// is starred already. On Gmail, conversation rows change as whole
// conversations, as a single a, u or * on one does. Archived ones leave the
// selection with the list; after u and * the selection stays.
//
// When the selection reaches messages that are not loaded (A over a whole
// folder or filter), it asks first: those are not on screen to look at.
func (ra *RootApp) actOnMarked(action ui.Action, envs []*mail.Envelope) {
	acct, folder := ra.manager.Active(), ra.currentFolder
	if acct == nil || folder == nil || !ra.maillist.MarksBeyondLoaded() {
		ra.doMarked(action, envs)
		return
	}
	what := ra.countText(acct, folder, len(envs))
	var question, label string
	switch action {
	case ui.ActionArchive:
		question, label = "Archive "+what+"?", "Archive"
	case ui.ActionMarkUnread:
		question, label = "Mark "+what+" unread?", "Mark unread"
	case ui.ActionStar:
		question, label = "Star "+what+"?", "Star"
		if !slices.ContainsFunc(envs, func(e *mail.Envelope) bool { return !e.IsFlagged() }) {
			question, label = "Remove the star from "+what+"?", "Unstar"
		}
	default:
		return
	}
	ra.showDeleteModal(question, []string{label, "Cancel"}, func() { ra.doMarked(action, envs) })
}

// doMarked carries out actOnMarked.
func (ra *RootApp) doMarked(action ui.Action, envs []*mail.Envelope) {
	acct, folder := ra.manager.Active(), ra.currentFolder
	if acct == nil || folder == nil {
		return
	}
	eng, ok := ra.engines[acct.Name]
	if !ok {
		return
	}
	th, threads := eng.(msync.Threads)
	threads = threads && ra.conversationRows(acct.Name, folder.Name)
	count := func(n int) string { return ra.countText(acct, folder, n) }
	thread := func(ctx context.Context, env *mail.Envelope, add, remove []string) (bool, error) {
		if threads && env.ThreadID != "" {
			return true, th.ModifyThread(ctx, env.ThreadID, add, remove)
		}
		return false, nil
	}
	src := folder.Name
	switch action {
	case ui.ActionArchive:
		mv, isMover := eng.(msync.Mover)
		if !threads && !isMover {
			ra.uiApp.StatusBar.Info(archiveHint)
			return
		}
		ra.applyToMarked("archive", acct, eng, folder, envs,
			func(n int) string { return "Archived " + count(n) },
			func(ctx context.Context, env *mail.Envelope) error {
				if did, err := thread(ctx, env, nil, []string{"INBOX"}); did {
					return err
				}
				if !isMover {
					return errors.New("archive: not available for this account")
				}
				return mv.Archive(ctx, src, env.ID)
			}, nil)
	case ui.ActionMarkUnread:
		ra.applyToMarked("unread", acct, eng, folder, envs,
			func(n int) string { return "Marked " + count(n) + " unread" },
			func(ctx context.Context, env *mail.Envelope) error {
				if did, err := thread(ctx, env, nil, []string{`\Seen`}); did {
					return err
				}
				return eng.SetFlags(ctx, src, env.ID, []string{`\Seen`}, false)
			}, nil, true)
	case ui.ActionStar:
		on := slices.ContainsFunc(envs, func(e *mail.Envelope) bool { return !e.IsFlagged() })
		done := "Starred "
		add, remove := []string{`\Flagged`}, []string(nil)
		if !on {
			done, add, remove = "Star removed from ", nil, []string{`\Flagged`}
		}
		ra.applyToMarked("star", acct, eng, folder, envs,
			func(n int) string { return done + count(n) },
			func(ctx context.Context, env *mail.Envelope) error {
				if did, err := thread(ctx, env, add, remove); did {
					return err
				}
				return eng.SetFlags(ctx, src, env.ID, []string{`\Flagged`}, on)
			}, nil, true)
	}
}

// bulkProgressLog is how often a running bulk action logs how far it got;
// the status bar says it every tick.
const bulkProgressLog = 10

// bulkProgress shows, every tick until stop is called, how many of total a
// bulk action has reached ("Deleting 120 of 465 messages…"), and logs it
// every bulkProgressLog ticks.
func (ra *RootApp) bulkProgress(acct *mail.Account, tag, total string, reached *atomic.Int64) (stop func()) {
	tick := ra.bulkTick
	if tick <= 0 {
		tick = time.Second
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for n := 1; ; n++ {
			select {
			case <-done:
				return
			case <-t.C:
			}
			msg := bulkDoing(tag, thousands(int(reached.Load()))+" of "+total)
			logIt := n%bulkProgressLog == 0
			ra.onUI(func() {
				if !ra.bulkBusy { // finished meanwhile: the result is on screen
					return
				}
				ra.uiApp.StatusBar.Progress(msg)
				if logIt {
					ui.AppLog.Info("[%s] %s", acct.Name, msg)
				}
			})
		}
	}()
	return func() { close(done) }
}

// bulkDoing is the status line while a bulk action runs.
func bulkDoing(tag, what string) string {
	switch tag {
	case "move":
		return "Moving " + what + "…"
	case "delete":
		return "Deleting " + what + "…"
	case "archive":
		return "Archiving " + what + "…"
	case "unread":
		return "Marking " + what + " unread…"
	case "star":
		return "Changing the star on " + what + "…"
	}
	return "Working on " + what + "…"
}

// failureText is a failed change's reason for the status bar.
func failureText(err error) string {
	if errors.Is(err, msync.ErrNotInTrash) {
		return "no longer in Trash"
	}
	return err.Error()
}

// forwardSubject is subject with "Fwd: " in front, unless it is already a
// forward.
func forwardSubject(subject string) string {
	lower := strings.ToLower(strings.TrimSpace(subject))
	if strings.HasPrefix(lower, "fwd:") || strings.HasPrefix(lower, "fw:") {
		return subject
	}
	return "Fwd: " + subject
}

// startForward opens the editor on a forward of msg. Its attachments are not
// carried (outgoing mail has no attachments yet): the draft names them, and
// so does the status bar.
func (ra *RootApp) startForward(msg *mail.Message) {
	acct := ra.manager.Active()
	if acct == nil || msg == nil || msg.Envelope == nil {
		return
	}
	if ra.conv != nil && !ra.conv.downloaded(msg) {
		ra.uiApp.StatusBar.Info(replyRefused) // never forward the placeholder
		return
	}
	d, dropped := buildForwardDraft(acct, msg)
	ra.launchEditor(d)
	if len(dropped) > 0 && ra.editorCommand() != "" { // else the status says no editor
		ra.uiApp.StatusBar.Info(fmt.Sprintf("Forward does not carry attachments: %s", strings.Join(dropped, ", ")))
	}
}
