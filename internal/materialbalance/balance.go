package materialbalance

import "sort"

// DefaultTolerance 是默认的损失判定容差（绝对量）。
const DefaultTolerance = 0.0

// cumulativePoints 是三个累积式流量计计量点，按固定顺序遍历。
var cumulativePoints = []Point{PointFeed, PointOverhead, PointBottoms}

// compute 是无锁的纯衡算：仅由窗口读数、切窗携带的期初存量与容差推导结果。
//
//	投入   = 期末进料表 − 期初进料表
//	产出   = 塔顶表差 + 塔底表差
//	净积累 = 期末存量 − 期初存量
//	损失   = 投入 − 产出 − 净积累
func compute(readings map[Point][]Reading, startInv *float64, tol float64) Balance {
	b := Balance{Tolerance: tol}

	b.TotalInput = cumulativeDelta(readings[PointFeed])
	b.OverheadOut = cumulativeDelta(readings[PointOverhead])
	b.BottomsOut = cumulativeDelta(readings[PointBottoms])
	b.TotalOutput = b.OverheadOut + b.BottomsOut

	inventory := sortedCopy(readings[PointInventory])

	switch {
	case startInv != nil:
		b.StartInventory = *startInv
		b.InventoryKnown = true
	case len(inventory) > 0:
		b.StartInventory = inventory[0].Value
		b.InventoryKnown = true
	}

	if len(inventory) > 0 {
		b.EndInventory = inventory[len(inventory)-1].Value
	} else if b.InventoryKnown {
		// 窗内没有期末存量读数，沿用最近已知（期初）存量，净积累按 0 处理。
		b.EndInventory = b.StartInventory
	}
	if b.InventoryKnown {
		b.Accumulation = b.EndInventory - b.StartInventory
	}

	b.Loss = b.TotalInput - b.TotalOutput - b.Accumulation

	for _, p := range cumulativePoints {
		if len(readings[p]) == 0 {
			b.MissingPoints = append(b.MissingPoints, p)
		}
	}
	// 完整性要求三个累积点在窗内至少各报一次，且窗内有设备存量读数。
	b.Complete = len(b.MissingPoints) == 0 && len(inventory) > 0
	b.Verdict = verdictFor(b.Loss, tol, b.Complete)
	return b
}

// verdictFor 依据损失与容差给出结论；边界 |loss| == tol 判定为 balanced。
func verdictFor(loss, tol float64, complete bool) string {
	if !complete {
		return VerdictIncomplete
	}
	if loss > tol {
		return VerdictLoss
	}
	if loss < -tol {
		return VerdictGain
	}
	return VerdictBalanced
}

// cumulativeDelta 返回某累积表在窗口内的首末差；不足两条读数时首条仅作基线，差为 0。
func cumulativeDelta(rs []Reading) float64 {
	if len(rs) < 2 {
		return 0
	}
	ordered := sortedCopy(rs)
	return ordered[len(ordered)-1].Value - ordered[0].Value
}

// sortedCopy 返回按时间戳升序排列的副本，不改动入参切片（读数按到达顺序存储）。
func sortedCopy(rs []Reading) []Reading {
	out := append([]Reading(nil), rs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}
