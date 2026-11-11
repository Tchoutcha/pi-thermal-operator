package controller

import (
	"context"
	"os"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	thermalv1alpha1 "github.com/Tchoutcha/pi-thermal-operator/api/thermal/v1alpha1"
)

const (
	annTemp       = "thermal.stellat.dev/temp-celsius"
	taintKey      = "thermal.stellat.dev/hot"
	policyName    = "default"
	reasonAdded   = "ThermalTaintAdded"
	reasonRemoved = "ThermalTaintRemoved"
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

// ---- Métriques Prometheus (exposées sur /metrics du contrôleur) ----

var (
	nodeTemp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pi_thermal_node_temperature_celsius",
		Help: "SoC temperature reported by the agent.",
	}, []string{"node"})
	nodeHot = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pi_thermal_node_hot",
		Help: "1 if the node has the thermal taint, 0 otherwise.",
	}, []string{"node"})
	taintChanges = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pi_thermal_taint_changes_total",
		Help: "Number of thermal taints added or removed.",
	}, []string{"action"})
)

func init() {
	metrics.Registry.MustRegister(nodeTemp, nodeHot, taintChanges)
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
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

// eventRecorder : nouvelle API d'événements (events.k8s.io)
type eventRecorder interface {
	Eventf(regarding runtime.Object, related runtime.Object, eventtype, reason, action, note string, args ...any)
}

type NodeReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder eventRecorder
}

func (r *NodeReconciler) event(n *corev1.Node, eventtype, reason, note string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(n, nil, eventtype, reason, "Reconcile", note, args...)
	}
}

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=thermal.stellat.dev,resources=thermalpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

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
		if apierrors.IsNotFound(err) { // nœud supprimé : on retire ses métriques
			nodeTemp.DeleteLabelValues(req.Name)
			nodeHot.DeleteLabelValues(req.Name)
		}
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
	nodeTemp.WithLabelValues(node.Name).Set(temp)

	hot, cool, err := r.thresholds(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	hasTaint := hasHotTaint(&node)
	act := decide(temp, hasTaint, hot, cool)
	if act == noop {
		nodeHot.WithLabelValues(node.Name).Set(boolToFloat(hasTaint))
		return ctrl.Result{}, nil
	}

	orig := node.DeepCopy()
	if act == addTaint {
		node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
			Key: taintKey, Value: "true", Effect: corev1.TaintEffectPreferNoSchedule,
		})
	} else {
		var kept []corev1.Taint
		for _, t := range node.Spec.Taints {
			if t.Key != taintKey {
				kept = append(kept, t)
			}
		}
		node.Spec.Taints = kept
	}

	if err := r.Patch(ctx, &node, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, err
	}

	if act == addTaint {
		nodeHot.WithLabelValues(node.Name).Set(1)
		taintChanges.WithLabelValues("added").Inc()
		log.Info("nœud trop chaud : taint ajouté", "node", node.Name, "temp", temp, "hot", hot)
		r.event(&node, corev1.EventTypeWarning, reasonAdded,
			"temperature %.1f °C >= %.0f °C: taint %s added", temp, hot, taintKey)
	} else {
		nodeHot.WithLabelValues(node.Name).Set(0)
		taintChanges.WithLabelValues("removed").Inc()
		log.Info("nœud refroidi : taint retiré", "node", node.Name, "temp", temp, "cool", cool)
		r.event(&node, corev1.EventTypeNormal, reasonRemoved,
			"temperature %.1f °C <= %.0f °C: taint %s removed", temp, cool, taintKey)
	}
	return ctrl.Result{}, nil
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
