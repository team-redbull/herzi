package controller

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/team-redbull/herzi/internal/config"
	"github.com/team-redbull/herzi/internal/kube"
	"github.com/team-redbull/herzi/internal/metrics"
	"github.com/team-redbull/herzi/internal/pipeline"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// world models what the real ports report: an accepted order shows up as open runs in every later
// sweep, exactly as the real sweeper sees the Temporal runs the route just started. A fake that kept
// reporting "no open runs" would make the controller (correctly) order the same machines again as soon
// as a fresh observation arrived.
type world struct {
	mu     sync.Mutex
	open   []pipeline.SlotRun
	sweeps int
	notify chan pipeline.InstallRequest
}

func newWorld() *world { return &world{notify: make(chan pipeline.InstallRequest, 100)} }

func (w *world) Sweep(context.Context, pipeline.PoolKey) pipeline.SweepResult {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sweeps++
	return pipeline.SweepResult{Slots: append([]pipeline.SlotRun(nil), w.open...), ObservedAt: time.Now()}
}

func (w *world) Install(_ context.Context, r pipeline.InstallRequest) (pipeline.InstallResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	started := make([]int, 0, r.Count)
	for i := 0; i < r.Count; i++ {
		slot := len(w.open) + 1
		w.open = append(w.open, pipeline.SlotRun{
			Slot: slot, Status: pipeline.StatusRunning, Started: time.Now(), RunID: fmt.Sprintf("run-%d", slot),
		})
		started = append(started, slot)
	}
	w.notify <- r
	return pipeline.InstallResult{Requested: r.Count, Started: started, HTTPCode: 202}, nil
}

func (w *world) sweepCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sweeps
}

// setInfraEnvAnnotations rewrites the InfraEnv's annotations from a FRESH read, retrying on conflict:
// the controller patches the same object (marker, state), so a stale copy would be refused.
func setInfraEnvAnnotations(pool string, annotations map[string]string) {
	Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ie := infra(pool)
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(ie), ie); err != nil {
			return err
		}
		ie.SetAnnotations(annotations)
		return k8sClient.Update(ctx, ie)
	})).To(Succeed())
}

var _ = Describe("Inventory watches", func() {
	It("grows, respects pause, watches replicas, and reloads every pool", func() {
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			Skip("KUBEBUILDER_ASSETS is unset")
		}
		for _, name := range []string{"inventory", "tenant"} {
			err := k8sClient.Create(ctx, &corev1.Namespace{Name: name})
			Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
		}
		deleteAfterSpec(agent("one", "p"), agent("two", "p"), infra("p"), node("workers", "p", 0))
		initial := testConfig()
		initial.Interval = 10 * time.Minute // must stay <= probeInterval or the pool is config_invalid
		store := config.NewStore(initial)
		reload := make(chan event.GenericEvent, 10)
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Cache:   CacheOptions("inventory"),
			Client:  client.Options{Cache: &client.CacheOptions{Unstructured: true}},
			Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", LeaderElection: false,
		})
		Expect(err).NotTo(HaveOccurred())
		w := newWorld()
		r, err := New(Options{
			Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Config: store,
			Sweeper:   w,
			Queues:    &queuesFake{result: pipeline.PollerStatus{Activity: 1, Workflow: 1, Segment: 1}},
			Installer: w, Metrics: metrics.New(prometheus.NewRegistry()), Reload: reload,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.SetupWithManager(mgr)).To(Succeed())
		managerCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(managerCtx) }()
		DeferCleanup(func() { stop(); Eventually(done).Should(Receive(BeNil())) })
		Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())

		// Two claimable Agents, demand 4: target 4 + max(minFree 2, 30% of 4) = 6, supply 2, so 4 are missing.
		for _, a := range []*unstructured.Unstructured{agent("one", "p"), agent("two", "p")} {
			status := a.Object["status"]
			Expect(k8sClient.Create(ctx, a)).To(Succeed())
			a.Object["status"] = status
			Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, node("workers", "p", 4))).To(Succeed())
		Eventually(mgr.GetClient().Get).WithArguments(ctx, client.ObjectKeyFromObject(node("workers", "p", 0)),
			node("workers", "p", 0)).Should(Succeed())
		Expect(k8sClient.Create(ctx, infra("p"))).To(Succeed())
		Eventually(w.notify, 10*time.Second).Should(Receive(Equal(pipeline.InstallRequest{Key: "p", Count: 4})))

		// Pause is persisted on the InfraEnv. Replicas 4 -> 7 makes target 9 against supply 2 + 4 open = 6
		// (3 missing), but a paused pool must not order.
		setInfraEnvAnnotations("p", map[string]string{kube.AnnotationPaused: "true"})
		Eventually(func() string {
			u := infra("p")
			if mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(u), u) != nil {
				return ""
			}
			return u.GetAnnotations()[kube.AnnotationPaused]
		}).Should(Equal("true"))
		for len(w.notify) > 0 {
			<-w.notify
		}
		np := node("workers", "p", 4)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(np), np)).To(Succeed())
		Expect(unstructured.SetNestedField(np.Object, int64(7), "spec", "replicas")).To(Succeed())
		Expect(k8sClient.Update(ctx, np)).To(Succeed())
		Consistently(w.notify, 1500*time.Millisecond, 100*time.Millisecond).ShouldNot(Receive())

		// Unpause: exactly the 3 missing machines are ordered, driven by the NodePool/InfraEnv watch.
		setInfraEnvAnnotations("p", nil)
		Eventually(w.notify, 10*time.Second).Should(Receive(Equal(pipeline.InstallRequest{Key: "p", Count: 3})))

		// Replicas 7 -> 8: target 8 + max(2, 30% of 8 = 2) = 10 against supply 2 + 4 + 3 = 9, so 1 is missing.
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(np), np)).To(Succeed())
		Expect(unstructured.SetNestedField(np.Object, int64(8), "spec", "replicas")).To(Succeed())
		Expect(k8sClient.Update(ctx, np)).To(Succeed())
		Eventually(w.notify, 10*time.Second).Should(Receive(Equal(pipeline.InstallRequest{Key: "p", Count: 1})))

		// Replicas 8 -> 10: target 13 against supply 10 orders three through the watch.
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(np), np)).To(Succeed())
		Expect(unstructured.SetNestedField(np.Object, int64(10), "spec", "replicas")).To(Succeed())
		Expect(k8sClient.Update(ctx, np)).To(Succeed())
		Eventually(w.notify, 10*time.Second).Should(Receive(Equal(pipeline.InstallRequest{Key: "p", Count: 3})))

		// The pool is now exactly at target, so a config reload re-enqueues it (one more sweep) and orders nothing.
		before := w.sweepCount()
		reload <- event.GenericEvent{Object: infra("p")}
		Eventually(w.sweepCount, 10*time.Second).Should(BeNumerically(">", before))
		Consistently(w.notify, time.Second, 100*time.Millisecond).ShouldNot(Receive())
	})
})
