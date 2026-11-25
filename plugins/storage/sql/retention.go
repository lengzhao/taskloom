package sql

import (
	"context"
	"log/slog"
	"time"

	"gorm.io/gorm"
)

// pruneInterval is how often expired events and receipts are removed.
const pruneInterval = time.Hour

// eventFloor records the highest event sequence pruned so far; a change-feed
// cursor below it may have missed events. It lives beside the coordination
// counter, which is migrated on every start.
type eventFloor struct {
	ID    int `gorm:"primaryKey;autoIncrement:false"`
	Floor int64
}

func (s *Store) startPruner() {
	events, _ := time.ParseDuration(s.config.EventRetention)
	receipts, _ := time.ParseDuration(s.config.ReceiptRetention)
	ctx, cancel := context.WithCancel(context.Background())
	s.stopPrune, s.pruneDone = cancel, make(chan struct{})
	go func() {
		defer close(s.pruneDone)
		tick := time.NewTicker(pruneInterval)
		defer tick.Stop()
		for {
			if err := s.prune(ctx, time.Now(), events, receipts); err != nil && ctx.Err() == nil {
				slog.Default().WarnContext(ctx, "pruning expired events and receipts failed; retrying next interval", slog.String("error", err.Error()))
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

// stopPruner cancels the pruner and waits for it, bounded by ctx.
func (s *Store) stopPruner(ctx context.Context) error {
	if s.stopPrune == nil {
		return nil
	}
	s.stopPrune()
	select {
	case <-s.pruneDone:
		s.stopPrune = nil
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pruneBatch bounds the rows one cleanup transaction deletes, so the global
// write lock is released between batches even when a large backlog expires.
const pruneBatch = 1000

// prune deletes events and receipts older than their retention in bounded
// command transactions, raising the event floor to the last pruned sequence.
func (s *Store) prune(ctx context.Context, now time.Time, events, receipts time.Duration) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		more := false
		err := s.Do(ctx, func(tx context.Context) error {
			return s.use(tx, true, func(db *gorm.DB) error {
				var seqs []int64
				if err := db.Model(&eventRow{}).Where("created_at < ?", now.Add(-events)).Order("sequence ASC").Limit(pruneBatch).Pluck("sequence", &seqs).Error; err != nil {
					return err
				}
				if len(seqs) > 0 {
					last := seqs[len(seqs)-1]
					if err := db.Where("sequence <= ?", last).Delete(&eventRow{}).Error; err != nil {
						return err
					}
					if err := db.Model(&eventFloor{}).Where("id = ? AND floor < ?", 1, last).Update("floor", last).Error; err != nil {
						return err
					}
				}
				var keys []string
				if err := db.Model(&receiptRecord{}).Where("created_at < ?", now.Add(-receipts)).Limit(pruneBatch).Pluck("row_key", &keys).Error; err != nil {
					return err
				}
				if len(keys) > 0 {
					if err := db.Where("row_key IN ?", keys).Delete(&receiptRecord{}).Error; err != nil {
						return err
					}
				}
				more = len(seqs) == pruneBatch || len(keys) == pruneBatch
				return nil
			})
		})
		if err != nil || !more {
			return err
		}
	}
}

// EventFloor is the highest pruned event sequence; zero when nothing was pruned.
func (s *Store) EventFloor(ctx context.Context) (int64, error) {
	var f eventFloor
	err := s.use(ctx, false, func(db *gorm.DB) error { return db.First(&f, 1).Error })
	return f.Floor, err
}
