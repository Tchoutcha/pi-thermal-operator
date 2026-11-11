package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	thermalv1alpha1 "github.com/Tchoutcha/pi-thermal-operator/api/thermal/v1alpha1"
)

const testNode = "pi1"

// Faux recorder : garde la liste des raisons d'événements émis
type fakeRecorder struct{ reasons []string }

func (f *fakeRecorder) Eventf(_ runtime.Object, _ runtime.Object, _, reason, _, _ string, _ ...any) {
	f.reasons = append(f.reasons, reason)
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name     string
		temp     float64
		hasTaint bool
		want     action
	}{
		{"froid, pas de taint", 50, false, noop},
		{"chaud, pas de taint", 80, false, addTaint},
		{"pile au seuil chaud", 75, false, addTaint},
		{"chaud, déjà taint", 80, true, noop},
		{"zone hystérésis, pas de taint", 70, false, noop},
		{"zone hystérésis, taint gardé", 70, true, noop},
		{"refroidi, taint retiré", 60, true, removeTaint},
		{"pile au seuil froid", 68, true, removeTaint},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decide(c.temp, c.hasTaint, 75, 68); got != c.want {
				t.Errorf("decide(%v, %v) = %v, attendu %v", c.temp, c.hasTaint, got, c.want)
			}
		})
	}
}

func setup(t *testing.T, objs ...client.Object) (*NodeReconciler, client.Client, *fakeRecorder) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := thermalv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	rec := &fakeRecorder{}
	return &NodeReconciler{Client: c, Scheme: scheme, Recorder: rec}, c, rec
}

func reconcileNode(t *testing.T, r *NodeReconciler, c client.Client) *corev1.Node {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: testNode}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var n corev1.Node
	if err := c.Get(context.Background(), req.NamespacedName, &n); err != nil {
		t.Fatal(err)
	}
	return &n
}

func nodeAt(temp string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: testNode, Annotations: map[string]string{annTemp: temp}}}
}

// Cycle complet + un événement à chaque changement
func TestReconcileAddThenRemove(t *testing.T) {
	r, c, rec := setup(t, nodeAt("80"))

	n := reconcileNode(t, r, c)
	if !hasHotTaint(n) {
		t.Fatal("taint attendu à 80 °C")
	}

	n.Annotations[annTemp] = "60"
	if err := c.Update(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	n = reconcileNode(t, r, c)
	if hasHotTaint(n) {
		t.Fatal("taint devrait être retiré à 60 °C")
	}

	if len(rec.reasons) != 2 || rec.reasons[0] != reasonAdded || rec.reasons[1] != reasonRemoved {
		t.Fatalf("événements attendus [%s %s], obtenu %v", reasonAdded, reasonRemoved, rec.reasons)
	}
}

// Pas de changement → pas d'événement (évite de spammer le cluster)
func TestReconcileNoEventWhenNoChange(t *testing.T) {
	r, c, rec := setup(t, nodeAt("50"))
	reconcileNode(t, r, c)
	if len(rec.reasons) != 0 {
		t.Fatalf("aucun événement attendu, obtenu %v", rec.reasons)
	}
}

func TestReconcileUsesPolicy(t *testing.T) {
	policy := &thermalv1alpha1.ThermalPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: policyName},
		Spec:       thermalv1alpha1.ThermalPolicySpec{HotCelsius: 60, CoolCelsius: 55},
	}
	r, c, _ := setup(t, nodeAt("65"), policy)

	if n := reconcileNode(t, r, c); !hasHotTaint(n) {
		t.Fatal("taint attendu : 65 °C >= seuil de la politique (60 °C)")
	}
}

func TestReconcileKeepsOtherTaints(t *testing.T) {
	other := corev1.Taint{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule}
	node := nodeAt("60")
	node.Spec.Taints = []corev1.Taint{
		other,
		{Key: taintKey, Value: "true", Effect: corev1.TaintEffectPreferNoSchedule},
	}
	r, c, _ := setup(t, node)

	n := reconcileNode(t, r, c)
	if hasHotTaint(n) {
		t.Fatal("taint thermique devrait être retiré")
	}
	if len(n.Spec.Taints) != 1 || n.Spec.Taints[0].Key != other.Key {
		t.Fatalf("l'autre taint doit rester, obtenu %v", n.Spec.Taints)
	}
}

func TestReconcileIgnoresBadInput(t *testing.T) {
	for _, ann := range []map[string]string{nil, {annTemp: "abc"}} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testNode, Annotations: ann}}
		r, c, _ := setup(t, node)
		if n := reconcileNode(t, r, c); len(n.Spec.Taints) != 0 {
			t.Fatalf("aucun taint attendu, obtenu %v", n.Spec.Taints)
		}
	}
}
