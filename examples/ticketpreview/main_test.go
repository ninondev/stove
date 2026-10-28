// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package main

import "testing"

const want = `桌台号：Demo 1
堂口单
Stove Demo Kitchen-堂食
菜品                                   3人  数量
------------------------------------------------
宫保鸡丁                                    1/份
麻婆豆腐                                    1/份
鱼香茄子                                    1/份
------------------------------------------------
操作员：Demo Operator
打印时间：2026/10/01 18:30:00
厨房
流水号 No.00001
`

func TestPreviewText(t *testing.T) {
	if got := textPreview(samplePayload()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
