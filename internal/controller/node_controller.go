package controller

import (
	"context"
	"os"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	annTemp  = "thermal.stellat.dev/temp-celsius"
	taintKey = "thermal.stellat.dev/hot"
)

var (
	hotC  = envFloat("THERMAL_HOT_C", 75)
	coolC = envFloat("THERMAL_COOL_C", 68)
)

func envFloat(name string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil {
		return v
	}
	return def
}

// ---- Décision (logique pure, testée unitairement) ----

type action int

const (
	noop action = iota
	addTaint
	removeTaint
)

func decide(temp float64, hasTaint bool, hot, cool float64) action {
	switch {
	case temp >= hot && !hasTaint:
		return addTaint
	case temp <= cool && hasTaint:
		return removeTaint
	}
	return noop
}

func hasHotTaint(n *corev1.Node) bool {
	for _, t := range n.Spec.Taints {
		if t.Key == taintKey {
			return true
		}
	}
	return false
}

// ---- Contrôleur ----

type NodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch

func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	s, ok := node.Annotations[annTemp]
	if !ok {
		return ctrl.Result{}, nil
	}
	temp, err := strconv.ParseFloat(s, 64)
	if err != nil {
		log.Info("température illisible", "node", node.Name, "value", s)
		return ctrl.Result{}, nil
	}

	orig := node.DeepCopy()
	switch decide(temp, hasHotTaint(&node), hotC, coolC) {
	case addTaint:
		node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
			Key: taintKey, Value: "true", Effect: corev1.TaintEffectPreferNoSchedule,
		})
		log.Info("nœud trop chaud : taint ajouté", "node", node.Name, "temp", temp)
	case removeTaint:
		var kept []corev1.Taint
		for _, t := range node.Spec.Taints {
			if t.Key != taintKey {
				kept = append(kept, t)
			}
		}
		node.Spec.Taints = kept
		log.Info("nœud refroidi : taint retiré", "node", node.Name, "temp", temp)
	default:
		return ctrl.Result{}, nil
	}

	err = r.Patch(ctx, &node, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))
	return ctrl.Result{}, err
}

func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Named("node").
		Complete(r)
}
