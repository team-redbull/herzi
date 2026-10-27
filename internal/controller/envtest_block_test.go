package controller

import (
	"context"
	"fmt"
	"os"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var skipNameValidation = true

// deleteAfterSpec removes the objects a spec created. It is registered BEFORE the manager starts, so
// (cleanups run last-in first-out) the objects go only after the manager has stopped, and no later
// spec sees them.
func deleteAfterSpec(objects ...*unstructured.Unstructured) {
	DeferCleanup(func() {
		for _, o := range objects {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, o))).To(Succeed())
		}
	})
}

// requestCount is the number of install requests received so far.
func (i *installFake) requestCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.requests)
}

// This spec proves, against a real kube-apiserver, the riskiest persistence path of the design:
// after an UNCLEAR install outcome the pool is blocked by an annotation written with a merge patch,
// the patch touches nothing else on the InfraEnv, and the very next reconcile does not order again.
var _ = Describe("Block after an unclear install", func() {
	It("writes only the annotation and stops further orders", func() {
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			Skip("KUBEBUILDER_ASSETS is unset")
		}
		for _, name := range []string{"inventory", "tenant"} {
			err := k8sClient.Create(ctx, &corev1.Namespace{Name: name})
			Expect(client.IgnoreAlreadyExists(err)).To(Succeed())
		}
		deleteAfterSpec(infra("q"), node("workers-q", "q", 0))
		initial := testConfig()
		initial.Interval = 10 * time.Minute // must stay <= probeInterval or the pool is config_invalid
		initial.AmbiguousWait = time.Hour
		store := config.NewStore(initial)
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Cache:   CacheOptions("inventory"),
			Client:  client.Options{Cache: &client.CacheOptions{Unstructured: true}},
			Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
			// A second manager in the same test process: allow the controller name to repeat.
			Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
		})
		Expect(err).NotTo(HaveOccurred())
		installer := &installFake{
			notify: make(chan pipeline.InstallRequest, 100),
			err:    fmt.Errorf("transport: %w", pipeline.ErrAmbiguous),
		}
		r, err := New(Options{
			Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Config: store,
			Sweeper:   &sweepFake{now: time.Now}, // stamps each observation like the real sweeper
			Queues:    &queuesFake{result: pipeline.PollerStatus{Activity: 1, Workflow: 1, Segment: 1}},
			Installer: installer, Metrics: metrics.New(prometheus.NewRegistry()),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.SetupWithManager(mgr)).To(Succeed())
		managerCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(managerCtx) }()
		DeferCleanup(func() { stop(); Eventually(done).Should(Receive(BeNil())) })
		Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())

		// A pool with a deficit, an unrelated annotation and spec content that must survive the patch.
		ie := infra("q")
		ie.SetAnnotations(map[string]string{"keep.example/me": "yes"})
		Expect(unstructured.SetNestedField(ie.Object, "kept", "spec", "pullSecretRef", "name")).To(Succeed())
		// The demand must be in the cache BEFORE the pool exists, or the first reconcile sees none.
		workers := node("workers-q", "q", 3)
		Expect(k8sClient.Create(ctx, workers)).To(Succeed())
		Eventually(func() error {
			return mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(workers), node("workers-q", "q", 0))
		}, 10*time.Second, 50*time.Millisecond).Should(Succeed())
		Expect(k8sClient.Create(ctx, ie)).To(Succeed())

		// First reconcile orders once; the outcome is unclear.
		Eventually(installer.notify, 10*time.Second).Should(Receive(Equal(pipeline.InstallRequest{Key: "q", Count: 5})))

		// The annotation lands on the real object, in the future, and nothing else changed.
		var blockedUntil time.Time
		Eventually(func() error {
			got := infra("q")
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(got), got); err != nil {
				return err
			}
			value, ok := got.GetAnnotations()[kube.AnnotationBlockedUntil]
			if !ok {
				return fmt.Errorf("annotation %s not written yet", kube.AnnotationBlockedUntil)
			}
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return fmt.Errorf("annotation is not RFC 3339: %w", err)
			}
			blockedUntil = parsed
			Expect(got.GetAnnotations()).To(HaveKeyWithValue("keep.example/me", "yes"))
			name, _, _ := unstructured.NestedString(got.Object, "spec", "pullSecretRef", "name")
			Expect(name).To(Equal("kept"))
			return nil
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
		Expect(blockedUntil).To(BeTemporally(">", time.Now().Add(30*time.Minute)),
			"a one hour wait must be written as a future time")

		// More events for the same pool must not order again while the block stands.
		np := node("workers-q", "q", 3)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(np), np)).To(Succeed())
		Expect(unstructured.SetNestedField(np.Object, int64(9), "spec", "replicas")).To(Succeed())
		Expect(k8sClient.Update(ctx, np)).To(Succeed())
		Consistently(installer.notify, 2*time.Second, 100*time.Millisecond).ShouldNot(Receive())
		Expect(installer.requestCount()).To(Equal(1))

		// A stale annotation does not block: move it to the past and the pool orders again.
		past := infra("q")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(past), past)).To(Succeed())
		annotations := past.GetAnnotations()
		annotations[kube.AnnotationBlockedUntil] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		past.SetAnnotations(annotations)
		Expect(k8sClient.Update(ctx, past)).To(Succeed())
		Eventually(installer.notify, 10*time.Second).Should(Receive())
	})
})
