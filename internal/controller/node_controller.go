package controller

import (
	"context"
	"os"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	thermalv1alpha1 "github.com/Tchoutcha/pi-thermal-operator/api/thermal/v1alpha1"
)

const (
	annTemp    = "thermal.stellat.dev/temp-celsius"
	taintKey   = "thermal.stellat.dev/hot"
	policyName = "default"
)

// Seuils par défaut quand aucune ThermalPolicy n'existe
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

// ---- Décision (logique pure) ----

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
// +kubebuilder:rbac:groups=thermal.stellat.dev,resources=thermalpolicies,verbs=get;list;watch

// thresholds lit la ThermalPolicy "default", ou garde les valeurs par défaut.
func (r *NodeReconciler) thresholds(ctx context.Context) (float64, float64, error) {
	var p thermalv1alpha1.ThermalPolicy
	err := r.Get(ctx, types.NamespacedName{Name: policyName}, &p)
	if apierrors.IsNotFound(err) {
		return hotC, coolC, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return float64(p.Spec.HotCelsius), float64(p.Spec.CoolCelsius), nil
}

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

	hot, cool, err := r.thresholds(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	orig := node.DeepCopy()
	switch decide(temp, hasHotTaint(&node), hot, cool) {
	case addTaint:
		node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
			Key: taintKey, Value: "true", Effect: corev1.TaintEffectPreferNoSchedule,
		})
		log.Info("nœud trop chaud : taint ajouté", "node", node.Name, "temp", temp, "hot", hot)
	case removeTaint:
		var kept []corev1.Taint
		for _, t := range node.Spec.Taints {
			if t.Key != taintKey {
				kept = append(kept, t)
			}
		}
		node.Spec.Taints = kept
		log.Info("nœud refroidi : taint retiré", "node", node.Name, "temp", temp, "cool", cool)
	default:
		return ctrl.Result{}, nil
	}

	err = r.Patch(ctx, &node, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))
	return ctrl.Result{}, err
}

// allNodes : quand la politique change, tous les nœuds sont réévalués.
func (r *NodeReconciler) allNodes(ctx context.Context, _ client.Object) []reconcile.Request {
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(nodes.Items))
	for _, n := range nodes.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: n.Name}})
	}
	return reqs
}

func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Watches(&thermalv1alpha1.ThermalPolicy{}, handler.EnqueueRequestsFromMapFunc(r.allNodes)).
		Named("node").
		Complete(r)
}
