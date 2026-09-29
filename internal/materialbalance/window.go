package materialbalance

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
)

// window 是一个核算时段的内部状态。相邻窗口通过 startInv 保持设备存量连续：
// 上一窗的期末存量成为下一窗的期初存量。结算状态以 meta.Settled 为唯一事实源。
type window struct {
	meta      Meta
	readings  map[Point][]Reading // 按到达顺序存储，计算时再排序
	startInv  *float64            // 切窗时从上一窗携带；nil 表示尚无存量证据
	original  *Balance            // 首次结算时冻结，此后不再改动
	revisions []Revision
}

func newWindow(seq int, at int64, source string, feedRevision uuid.UUID, c model.Campaign, carry *float64) *window {
	return &window{
		meta: Meta{
			Sequence:     seq,
			CampaignID:   c.ID,
			Revision:     c.Revision,
			FeedRevision: feedRevision,
			Source:       source,
			OpenedAt:     at,
		},
		readings: make(map[Point][]Reading),
		startInv: carry,
	}
}

// add 校验并追加一条读数。累积表的单调性按时间序邻居判断，与到达顺序无关。
func (w *window) add(r Reading) error {
	if err := validateReading(r); err != nil {
		return err
	}
	if err := checkSequence(r.Point, sortedCopy(w.readings[r.Point]), r); err != nil {
		return err
	}
	w.readings[r.Point] = append(w.readings[r.Point], r)
	return nil
}

// contains 判断时间戳是否落在本窗。窗口相邻且共享切换时刻，采用半开区间：
// 非末窗覆盖 [OpenedAt, ClosedAt)，其后继覆盖 [ClosedAt, …)；
// 最终（停机）窗为 [OpenedAt, ClosedAt] 闭区间，使停机瞬间的读数仍计入末窗。
func (w *window) contains(at int64, final bool) bool {
	if at < w.meta.OpenedAt {
		return false
	}
	if !w.meta.Settled {
		return true
	}
	if final {
		return at <= w.meta.ClosedAt
	}
	return at < w.meta.ClosedAt
}

func (w *window) current(tol float64) Balance {
	return compute(w.readings, w.startInv, tol)
}

// provisional 表示已结算但窗内没有设备存量读数，期末只能沿用最近已知存量。
func (w *window) provisional() bool {
	return w.meta.Settled && len(w.readings[PointInventory]) == 0
}

// settle 首次结算时冻结原结论；重复结算幂等，返回的 bool 表示是否为本次新冻结。
func (w *window) settle(at int64, tol float64) (Balance, bool) {
	if w.meta.Settled {
		return *w.original, false
	}
	bal := w.current(tol)
	w.meta.ClosedAt = at
	w.meta.Settled = true
	w.original = &bal
	return bal, true
}

// endingCarry 给出本窗期末设备存量，供下一窗为期初存量：
// 优先窗内最晚一次存量读数，否则沿用期初 carry，均无则为 nil。
func (w *window) endingCarry() *float64 {
	if inv := sortedCopy(w.readings[PointInventory]); len(inv) > 0 {
		value := inv[len(inv)-1].Value
		return &value
	}
	if w.startInv != nil {
		value := *w.startInv
		return &value
	}
	return nil
}

// appendRevision 为一次迟到读数追加修订记录；Original 保持冻结值不变。
func (w *window) appendRevision(revisedAt int64, r Reading, tol float64) Revision {
	rev := Revision{
		Sequence: w.meta.Sequence,
		At:       revisedAt,
		Reason:   ReasonLateReading,
		Reading:  r,
		Original: *w.original,
		Revised:  w.current(tol),
	}
	w.revisions = append(w.revisions, rev)
	return rev
}

func (w *window) view(tol float64) WindowView {
	v := WindowView{
		Meta:        w.meta,
		Current:     w.current(tol),
		Provisional: w.provisional(),
		Revisions:   append([]Revision(nil), w.revisions...),
	}
	if w.original != nil {
		original := *w.original
		v.Original = &original
	}
	return v
}
