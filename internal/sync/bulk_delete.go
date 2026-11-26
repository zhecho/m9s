package sync

import (
	"context"
	"slices"
)

// permanentDeleteMany is BulkDeleter for an engine: inTrash lists the ids
// in the trash on the server, del deletes up to chunk of them for good, and
// each chunk is forgotten once gone. An id the listing does not hold —
// restored meanwhile, or deleted already — goes through one (the engine's
// PermanentDelete), which tells the two apart.
func permanentDeleteMany(ctx context.Context, ids []string, chunk int, progress func(int),
	inTrash func(context.Context) ([]string, error),
	del func(context.Context, []string) error,
	forget func([]string) error,
	one func(context.Context, string) error,
) map[string]error {
	if progress == nil {
		progress = func(int) {}
	}
	failed := make(map[string]error)
	listed, err := inTrash(ctx)
	if err != nil {
		for _, id := range ids {
			failed[id] = err
		}
		progress(len(ids))
		return failed
	}
	there := make(map[string]bool, len(listed))
	for _, id := range listed {
		there[id] = true
	}
	var batch, rest []string
	for _, id := range ids {
		if there[id] {
			batch = append(batch, id)
		} else {
			rest = append(rest, id)
		}
	}
	for part := range slices.Chunk(batch, chunk) {
		if err := del(ctx, part); err != nil {
			for _, id := range part {
				failed[id] = err
			}
		} else if err := forget(part); err != nil {
			for _, id := range part { // gone on the server; the poll drops the rows
				failed[id] = err
			}
		}
		progress(len(part))
	}
	for _, id := range rest {
		if err := one(ctx, id); err != nil {
			failed[id] = err
		}
		progress(1)
	}
	return failed
}
