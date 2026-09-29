package material

import (
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/google/uuid"
)

// Ledger 是物料衡算台账：维护按时段切开的衡算窗口、计量点读数，
// 以及迟到读数/更正对已结账时段的修订痕迹。
type Ledger struct {
	mu       sync.Mutex
	campaign uuid.UUID
	windows  []*window
	readings map[int64]Reading
	seq      int64
}

func NewLedger() *Ledger {
	return &Ledger{readings: make(map[int64]Reading)}
}

// Begin 切开一个新的衡算窗口（开车、换进料源）。
// 若上一窗口仍开放，则在 at 时刻结账并冻结其原结论；
// at 时刻的边界读数同时计入前后两窗，分别作为期末与期初基准。
func (l *Ledger) Begin(campaign, revision uuid.UUID, feedSource string, feedRevision uuid.UUID, at int64, reason string) (Balance, error) {
	if campaign == uuid.Nil {
		return Balance{}, errors.New("campaign is required")
	}
	if revision == uuid.Nil {
		return Balance{}, errors.New("campaign revision is required")
	}
	if feedSource == "" {
		return Balance{}, errors.New("feed source is required")
	}
	if at <= 0 {
		return Balance{}, errors.New("window timestamp is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var splitFrom *window
	if len(l.windows) > 0 {
		last := l.windows[len(l.windows)-1]
		if last.closed {
			// 停机后重新开车：与上一时段之间存在停产间隔，
			// 存量可能已变化，不跨停机结转边界读数。
			if at < last.endAt {
				return Balance{}, errors.New("balance window boundary must not regress")
			}
		} else {
			if at < last.startAt {
				return Balance{}, errors.New("balance window must not regress")
			}
			// 连续运行中切开：前窗因新时段开设而结账，边界读数连续结转。
			last.closedReason = reason
			last.close(at)
			splitFrom = last
		}
	}
	if l.campaign == uuid.Nil {
		l.campaign = campaign
	}
	next := &window{
		id:           fmt.Sprintf("W%03d", len(l.windows)+1),
		reason:       reason,
		revision:     revision,
		feedSource:   feedSource,
		feedRevision: feedRevision,
		startAt:      at,
		endAt:        at,
	}
	// 边界读数结转：仅连续运行中的切窗（开车/换进料源）才把切窗时刻
	// 各计量点最后一条读数同时作为前窗期末与新窗期初基准；
	// Seq 保持一致，更正时两侧一并修订。停机后重新开车不结转。
	if splitFrom != nil {
		carry := make(map[Point]Reading)
		for _, r := range splitFrom.readings {
			if r.At <= at {
				carry[r.Point] = r
			}
		}
		for _, r := range carry {
			entry := r
			entry.Window = next.id
			entry.FeedRevision = feedRevision
			next.Record(entry)
		}
	}
	l.windows = append(l.windows, next)
	return next.snapshot(), nil
}

// Close 在停机时刻结账当前窗口，不再开设新窗口。
// 结账时冻结原结论；此后到达的、测量时刻不晚于 at 的读数
// 仍作为迟到读数修订本时段。
func (l *Ledger) Close(at int64) (Balance, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.windows) == 0 {
		return Balance{}, errors.New("no balance window is open")
	}
	active := l.windows[len(l.windows)-1]
	if active.closed {
		return Balance{}, errors.New("balance window already closed")
	}
	if at < active.startAt {
		return Balance{}, errors.New("shutdown precedes balance window start")
	}
	active.closedReason = ReasonShutdown
	active.close(at)
	return active.snapshot(), nil
}

// RecordReading 登记一个计量点读数。读数按测量时刻 At 归入对应时段：
// 落在已结账窗口内即为迟到读数，立即修订该时段并重算；
// 边界时刻的读数同时进入相邻两个窗口。返回读数与命中的窗口编号。
func (l *Ledger) RecordReading(point Point, value float64, at, recorded int64) (Reading, []string, error) {
	reading := Reading{Point: point, Value: value, At: at, Recorded: recorded}
	if err := reading.Validate(); err != nil {
		return Reading{}, nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.windows) == 0 {
		return Reading{}, nil, errors.New("no balance window is open")
	}
	var touched []*window
	for _, w := range l.windows {
		if l.contains(w, at) {
			touched = append(touched, w)
		}
	}
	if len(touched) == 0 {
		return Reading{}, nil, errors.New("reading falls outside balance windows")
	}
	windowIDs := make([]string, 0, len(touched))

	// 同一计量点、同一时刻已有读数时，迟到的数值是对该观测的修正
	// （切窗边界上会同时重算前后两窗，缺失量在时段间转移、总量守恒），
	// 沿用原 Seq 覆盖；否则作为一条新的迟到读数追加。
	existingSeq := int64(0)
	for _, w := range touched {
		for _, r := range w.readings {
			if r.Point == point && r.At == at {
				existingSeq = r.Seq
				break
			}
		}
		if existingSeq != 0 {
			break
		}
	}
	if existingSeq != 0 {
		stored := l.readings[existingSeq]
		stored.Value = value
		stored.Recorded = recorded
		l.readings[existingSeq] = stored
		for _, w := range touched {
			beforeVerdict, beforeLoss := w.currentVerdict(), w.currentLoss()
			for i := range w.readings {
				if w.readings[i].Seq == existingSeq {
					w.readings[i].Value = value
					w.readings[i].Recorded = recorded
				}
			}
			w.recompute()
			if w.closed {
				w.amendments = append(w.amendments, Amendment{
					Seq: existingSeq, Point: point, At: at, Recorded: recorded, Reason: "late-revision",
					PreviousVerdict: beforeVerdict, PreviousLoss: beforeLoss,
					RevisedVerdict: w.currentVerdict(), RevisedLoss: w.currentLoss(),
				})
			}
			windowIDs = append(windowIDs, w.id)
		}
		return stored, windowIDs, nil
	}

	l.seq++
	reading.Seq = l.seq
	l.readings[reading.Seq] = reading
	for _, w := range touched {
		entry := reading
		entry.Window = w.id
		entry.FeedRevision = w.feedRevision
		if w.closed {
			// 迟到读数：结账窗口被修订，先记录修订前的当期结论。
			beforeVerdict, beforeLoss := w.currentVerdict(), w.currentLoss()
			w.Record(entry)
			w.recompute()
			w.amendments = append(w.amendments, Amendment{
				Seq: reading.Seq, Point: point, At: at, Recorded: recorded, Reason: "late-reading",
				PreviousVerdict: beforeVerdict, PreviousLoss: beforeLoss,
				RevisedVerdict: w.currentVerdict(), RevisedLoss: w.currentLoss(),
			})
		} else {
			w.Record(entry)
			w.recompute()
		}
		windowIDs = append(windowIDs, w.id)
	}
	return reading, windowIDs, nil
}

// CorrectReading 更正既有读数。含该读数的所有时段（含因边界共享
// 进入的相邻窗口与已结账窗口）全部重算，每次更正留下修订痕迹，
// 窗口关闭时冻结的原结论保持不变。
func (l *Ledger) CorrectReading(seq int64, value float64, recorded int64, reason string) ([]Balance, error) {
	if reason == "" {
		return nil, errors.New("correction reason is required")
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return nil, errors.New("meter reading is not a non-negative number")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	original, ok := l.readings[seq]
	if !ok {
		return nil, errors.New("reading not found")
	}
	original.Value = value
	l.readings[seq] = original

	affected := make([]Balance, 0)
	for _, w := range l.windows {
		var index int = -1
		for i := range w.readings {
			if w.readings[i].Seq == seq {
				index = i
				break
			}
		}
		if index < 0 {
			continue
		}
		beforeVerdict, beforeLoss := w.currentVerdict(), w.currentLoss()
		w.readings[index].Value = value
		w.recompute()
		if w.closed {
			w.amendments = append(w.amendments, Amendment{
				Seq: seq, Point: w.readings[index].Point, At: w.readings[index].At, Recorded: recorded, Reason: reason,
				PreviousVerdict: beforeVerdict, PreviousLoss: beforeLoss,
				RevisedVerdict: w.currentVerdict(), RevisedLoss: w.currentLoss(),
			})
		}
		affected = append(affected, w.snapshot())
	}
	if len(affected) == 0 {
		return nil, errors.New("reading falls outside balance windows")
	}
	return affected, nil
}

// contains 判断时刻 at 是否落在窗口内。已结账窗口为闭区间，
// 使边界读数同时属于前后两个窗口；开放窗口接收起点之后的全部读数。
func (l *Ledger) contains(w *window, at int64) bool {
	if at < w.startAt {
		return false
	}
	if !w.closed {
		return true
	}
	return at <= w.endAt
}

// Balances 返回全部时段按时序排列的衡算结果（含修订痕迹）。
func (l *Ledger) Balances() []Balance {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Balance, 0, len(l.windows))
	for _, w := range l.windows {
		w.recompute()
		out = append(out, w.snapshot())
	}
	return out
}

// ActiveBalance 返回当前尚未结账的窗口衡算。
func (l *Ledger) ActiveBalance() (Balance, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, w := range l.windows {
		if !w.closed {
			w.recompute()
			return w.snapshot(), true
		}
	}
	return Balance{}, false
}

func (l *Ledger) Reading(seq int64) (Reading, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.readings[seq]
	return r, ok
}
