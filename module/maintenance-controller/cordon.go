package main

// cordon.go：执行面（W43，0021 已学）——"受控 cordon"：同一次 Node 更新写
// unschedulable 与所有权标记，不拆两步。W44 原样保留，不重做练习。

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ApplyCordon：同一次 Update 写两个字段。
// 前置（调用方 Decide 已判定）：节点未被 cordon、无他人 marker、预检通过、UID 未变。
// node 是本轮现读的对象——它带的 resourceVersion 就是这次更新的条件凭据：
// 提交后若有人抢先写过，API Server 以 409 拒绝（IsConflict），调用方重读重判。
func ApplyCordon(ctx context.Context, c client.Client, plan *MaintenancePlan, node *corev1.Node) error {
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	node.Annotations[MarkerAnnotation] = string(plan.UID)
	node.Spec.Unschedulable = true
	return c.Update(ctx, node)
}
