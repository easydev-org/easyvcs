package store

import (
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// indexQueue decouples the path-index write (revision_path_dirs) from the commit
// transaction. The index is a DERIVED, additive table: it can be rebuilt from
// the revisions' persisted changed_paths at any time, so a small delay between a
// commit and its index rows is harmless as long as READS see their own writes.
//
// Writes enqueue (namespaceID, revisionID, changedPaths) after the revision row
// is committed. A background worker drains the queue in batches. RevisionsTouching
// calls Flush(namespaceID) first, so a query never misses a just-written
// revision (read-your-writes); other namespaces are unaffected.
type indexQueue struct {
	mu    sync.Mutex
	cond  *sync.Cond
	pends []indexJob
	// inFlight tracks, per namespace, how many jobs are still queued or being
	// applied, so Flush can wait for a specific namespace to drain.
	nsPending map[int64]int
}

type indexJob struct {
	namespaceID int64
	revisionID  string
	paths       []string
}

func newIndexQueue() *indexQueue {
	q := &indexQueue{nsPending: map[int64]int{}}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// enqueue adds a job and signals the worker.
func (q *indexQueue) enqueue(namespaceID int64, revisionID string, paths []string) {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.pends = append(q.pends, indexJob{namespaceID: namespaceID, revisionID: revisionID, paths: paths})
	q.nsPending[namespaceID]++
	q.mu.Unlock()
	q.cond.Signal()
}

// take removes and returns all pending jobs (called by the worker).
func (q *indexQueue) take() []indexJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.pends) == 0 {
		q.cond.Wait()
	}
	jobs := q.pends
	q.pends = nil
	return jobs
}

// done marks a batch applied, decrementing each namespace's pending count.
func (q *indexQueue) done(jobs []indexJob) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range jobs {
		q.nsPending[j.namespaceID]--
		if q.nsPending[j.namespaceID] <= 0 {
			delete(q.nsPending, j.namespaceID)
		}
	}
	q.cond.Broadcast()
}

// flush waits until namespaceID has no pending index jobs.
func (q *indexQueue) flush(namespaceID int64) {
	if q == nil {
		return
	}
	q.mu.Lock()
	for q.nsPending[namespaceID] > 0 {
		q.cond.Wait()
	}
	q.mu.Unlock()
}

// applyIndexBatch writes one batch of index rows (idempotent).
func (r *Repo) applyIndexBatch(jobs []indexJob) error {
	return r.cs.d.gdb.Transaction(func(tx *gorm.DB) error {
		for _, j := range jobs {
			if err := tx.Where("namespace_id=? AND revision_id=?", j.namespaceID, j.revisionID).
				Delete(&revisionPathDirRow{}).Error; err != nil {
				return err
			}
			dirs := changedPathDirs(j.paths)
			if len(dirs) == 0 {
				continue
			}
			rows := make([]revisionPathDirRow, 0, len(dirs))
			for d := range dirs {
				rows = append(rows, revisionPathDirRow{NamespaceID: j.namespaceID, Dir: d, RevisionID: j.revisionID})
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// indexWorker drains the queue in the background, applying batches.
func (s *CentralStore) indexWorker() {
	for {
		jobs := s.idxQueue.take()
		// Group by namespace to apply with the right repo handle; a batch is
		// small (one commit's revisions), so a per-job repo open is fine.
		byNS := map[int64][]indexJob{}
		for _, j := range jobs {
			byNS[j.namespaceID] = append(byNS[j.namespaceID], j)
		}
		for nsID, js := range byNS {
			repo := &Repo{cs: s, namespaceID: nsID}
			_ = repo.applyIndexBatch(js) // best-effort; rebuildable from changed_paths
		}
		s.idxQueue.done(jobs)
	}
}
