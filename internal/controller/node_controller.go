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

// Seuils (modifiables par variables d'environnement pour les tests)
var (
	hotC  = envFloat("THERMAL_HOT_C", 75)  // au-dessus : taint
	coolC = envFloat("THERMAL_COOL_C", 68) // en dessous : retrait du taint
)

func envFloat(name string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil {
		return v
	}
	return def
}

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
		return ctrl.Result{}, nil // agent pas encore passé sur ce nœud
	}
	temp, err := strconv.ParseFloat(s, 64)
	if err != nil {
		log.Info("température illisible", "node", node.Name, "value", s)
		return ctrl.Result{}, nil
	}

	hasTaint := false
	for _, t := range node.Spec.Taints {
		if t.Key == taintKey {
			hasTaint = true
			break
		}
	}

	orig := node.DeepCopy()
	switch {
	case temp >= hotC && !hasTaint:
		node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
			Key: taintKey, Value: "true", Effect: corev1.TaintEffectPreferNoSchedule,
		})
		log.Info("nœud trop chaud : taint ajouté", "node", node.Name, "temp", temp)
	case temp <= coolC && hasTaint:
		var kept []corev1.Taint
		for _, t := range node.Spec.Taints {
			if t.Key != taintKey {
				kept = append(kept, t)
			}
		}
		node.Spec.Taints = kept
		log.Info("nœud refroidi : taint retiré", "node", node.Name, "temp", temp)
	default:
		return ctrl.Result{}, nil // rien à changer
	}

	err = r.Patch(ctx, &node, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))
	return ctrl.Result{}, err // en cas de conflit, Kubernetes relance automatiquement
}

func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Named("node").
		Complete(r)
}
